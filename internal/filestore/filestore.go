// Package filestore provides persistent file storage with TTL and garbage collection.
package filestore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

// FileInfo represents metadata about a stored file.
type FileInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Path      string `json:"path"`                 // Filesystem path to blob
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at,omitempty"` // 0 = permanent
}

// Store manages file storage with SQLite metadata and flat blob storage.
type Store struct {
	db      *sql.DB
	blobDir string
	mu      sync.RWMutex

	// GC settings
	gcInterval time.Duration
	gcStop     chan struct{}
	gcWg       sync.WaitGroup
}

// New creates a new file store at the given base directory.
// Creates {baseDir}/files.db for metadata and {baseDir}/blobs/ for file data.
func New(baseDir string) (*Store, error) {
	blobDir := filepath.Join(baseDir, "blobs")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create blob dir: %w", err)
	}

	dbPath := filepath.Join(baseDir, "files.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Create schema
	if err := createSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create schema: %w", err)
	}

	s := &Store{
		db:         db,
		blobDir:    blobDir,
		gcInterval: 5 * time.Minute,
		gcStop:     make(chan struct{}),
	}

	// Start GC goroutine
	s.gcWg.Add(1)
	go s.gcLoop()

	return s, nil
}

func createSchema(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS files (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		size INTEGER NOT NULL,
		sha256 TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_files_expires ON files(expires_at) WHERE expires_at > 0;
	CREATE INDEX IF NOT EXISTS idx_files_sha256 ON files(sha256);
	`
	_, err := db.Exec(schema)
	return err
}

// Import copies a file into the store and returns its UUID.
// If ttl is 0, the file is permanent. Otherwise, ttl is seconds until expiration.
func (s *Store) Import(sourcePath string, name string, ttl int64) (*FileInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Open source file
	src, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open source: %w", err)
	}
	defer src.Close()

	// Get file info (for future use, e.g., preserving mtime)
	if _, err := src.Stat(); err != nil {
		return nil, fmt.Errorf("failed to stat source: %w", err)
	}

	// Generate UUID
	id := uuid.New().String()

	// Create destination
	dstPath := filepath.Join(s.blobDir, id)
	dst, err := os.Create(dstPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create blob: %w", err)
	}
	defer dst.Close()

	// Copy and compute SHA256
	hasher := sha256.New()
	writer := io.MultiWriter(dst, hasher)

	size, err := io.Copy(writer, src)
	if err != nil {
		os.Remove(dstPath)
		return nil, fmt.Errorf("failed to copy file: %w", err)
	}

	hash := hex.EncodeToString(hasher.Sum(nil))

	// Calculate expiration
	now := time.Now().Unix()
	var expiresAt int64 = 0
	if ttl > 0 {
		expiresAt = now + ttl
	}

	// Use source filename if name not provided
	if name == "" {
		name = filepath.Base(sourcePath)
	}

	// Insert into database
	_, err = s.db.Exec(
		`INSERT INTO files (id, name, size, sha256, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, size, hash, now, expiresAt,
	)
	if err != nil {
		os.Remove(dstPath)
		return nil, fmt.Errorf("failed to insert record: %w", err)
	}

	return &FileInfo{
		ID:        id,
		Name:      name,
		Size:      size,
		SHA256:    hash,
		Path:      dstPath,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}, nil
}

// GetPath returns the blob path for a file ID.
// Returns empty string if file doesn't exist.
func (s *Store) GetPath(id string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM files WHERE id = ?`, id).Scan(&exists)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("file not found: %s", id)
	}
	if err != nil {
		return "", fmt.Errorf("database error: %w", err)
	}

	return filepath.Join(s.blobDir, id), nil
}

// Info returns metadata for a file.
func (s *Store) Info(id string) (*FileInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var info FileInfo
	err := s.db.QueryRow(
		`SELECT id, name, size, sha256, created_at, expires_at FROM files WHERE id = ?`,
		id,
	).Scan(&info.ID, &info.Name, &info.Size, &info.SHA256, &info.CreatedAt, &info.ExpiresAt)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("file not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	// Add blob path
	info.Path = filepath.Join(s.blobDir, id)

	return &info, nil
}

// List returns all files, optionally including expired ones.
func (s *Store) List(includeExpired bool) ([]*FileInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `SELECT id, name, size, sha256, created_at, expires_at FROM files`
	if !includeExpired {
		query += ` WHERE expires_at = 0 OR expires_at > ?`
	}
	query += ` ORDER BY created_at DESC`

	var rows *sql.Rows
	var err error
	if !includeExpired {
		rows, err = s.db.Query(query, time.Now().Unix())
	} else {
		rows, err = s.db.Query(query)
	}
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var files []*FileInfo
	for rows.Next() {
		var info FileInfo
		if err := rows.Scan(&info.ID, &info.Name, &info.Size, &info.SHA256, &info.CreatedAt, &info.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		info.Path = filepath.Join(s.blobDir, info.ID)
		files = append(files, &info)
	}

	return files, nil
}

// Rename updates the display name of a file.
func (s *Store) Rename(id string, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`UPDATE files SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("update failed: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("file not found: %s", id)
	}

	return nil
}

// Delete removes a file from storage and database.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.deleteLocked(id)
}

func (s *Store) deleteLocked(id string) error {
	// Delete from database first
	result, err := s.db.Exec(`DELETE FROM files WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete failed: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("file not found: %s", id)
	}

	// Delete blob (ignore error if already gone)
	blobPath := filepath.Join(s.blobDir, id)
	os.Remove(blobPath)

	return nil
}

// SetTTL updates the TTL for a file. ttl=0 makes it permanent.
func (s *Store) SetTTL(id string, ttl int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var expiresAt int64 = 0
	if ttl > 0 {
		expiresAt = time.Now().Unix() + ttl
	}

	result, err := s.db.Exec(`UPDATE files SET expires_at = ? WHERE id = ?`, expiresAt, id)
	if err != nil {
		return fmt.Errorf("update failed: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("file not found: %s", id)
	}

	return nil
}

// gcLoop runs the garbage collector periodically.
func (s *Store) gcLoop() {
	defer s.gcWg.Done()

	ticker := time.NewTicker(s.gcInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.runGC()
		case <-s.gcStop:
			return
		}
	}
}

// runGC removes expired files.
func (s *Store) runGC() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()

	// Find expired files
	rows, err := s.db.Query(
		`SELECT id FROM files WHERE expires_at > 0 AND expires_at <= ?`,
		now,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	var expired []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			expired = append(expired, id)
		}
	}

	// Delete each expired file
	for _, id := range expired {
		s.deleteLocked(id)
	}
}

// Stats returns storage statistics.
func (s *Store) Stats() (totalFiles int64, totalSize int64, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	err = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size), 0) FROM files`).Scan(&totalFiles, &totalSize)
	return
}

// Close shuts down the store and stops the GC.
func (s *Store) Close() error {
	close(s.gcStop)
	s.gcWg.Wait()
	return s.db.Close()
}
