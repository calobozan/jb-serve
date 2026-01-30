package files

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// FileRef represents a reference to a managed file
type FileRef struct {
	Ref       string `json:"ref"`
	URL       string `json:"url"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
	CreatedAt int64  `json:"created_at"`
}

// Manager handles file storage for uploads and outputs
type Manager struct {
	uploadDir  string
	outputDir  string
	outputRefs map[string]*FileRef // ref -> FileRef
	mu         sync.RWMutex
}

// NewManager creates a file manager with the given base directory
func NewManager(baseDir string) (*Manager, error) {
	uploadDir := filepath.Join(baseDir, "uploads")
	outputDir := filepath.Join(baseDir, "outputs")

	for _, dir := range []string{uploadDir, outputDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create dir %s: %w", dir, err)
		}
	}

	return &Manager{
		uploadDir:  uploadDir,
		outputDir:  outputDir,
		outputRefs: make(map[string]*FileRef),
	}, nil
}

// SaveUpload saves a multipart file to temp storage and returns the path
func (m *Manager) SaveUpload(file multipart.File, header *multipart.FileHeader) (string, error) {
	ext := filepath.Ext(header.Filename)
	filename := uuid.New().String() + ext
	path := filepath.Join(m.uploadDir, filename)

	dst, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("failed to save upload: %w", err)
	}

	return path, nil
}

// RegisterOutput registers an output file and returns a FileRef
// The file is copied to the outputs directory for serving
func (m *Manager) RegisterOutput(sourcePath string) (*FileRef, error) {
	// Get file info
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat output file: %w", err)
	}

	// Generate ref and destination path
	ext := filepath.Ext(sourcePath)
	ref := uuid.New().String()[:8] // Short ref for URLs
	filename := ref + ext
	destPath := filepath.Join(m.outputDir, filename)

	// Copy file to outputs dir
	src, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open source: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(destPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create output file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		os.Remove(destPath)
		return nil, fmt.Errorf("failed to copy output: %w", err)
	}

	// Detect media type
	mediaType := mime.TypeByExtension(ext)
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}

	fileRef := &FileRef{
		Ref:       ref,
		URL:       "/v1/files/" + filename,
		Path:      destPath,
		Size:      info.Size(),
		MediaType: mediaType,
		CreatedAt: time.Now().Unix(),
	}

	m.mu.Lock()
	m.outputRefs[ref] = fileRef
	m.mu.Unlock()

	return fileRef, nil
}

// GetOutput retrieves a file ref by its ref ID
func (m *Manager) GetOutput(ref string) (*FileRef, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	fileRef, ok := m.outputRefs[ref]
	return fileRef, ok
}

// GetOutputPath returns the full path for a filename in outputs dir
func (m *Manager) GetOutputPath(filename string) string {
	return filepath.Join(m.outputDir, filename)
}

// OutputDir returns the outputs directory path
func (m *Manager) OutputDir() string {
	return m.outputDir
}

// Cleanup removes a temporary upload file
func (m *Manager) Cleanup(path string) error {
	if filepath.Dir(path) != m.uploadDir {
		return nil
	}
	return os.Remove(path)
}

// CleanupAll removes multiple temporary files
func (m *Manager) CleanupAll(paths []string) {
	for _, p := range paths {
		m.Cleanup(p)
	}
}

// DeleteOutput removes an output file by ref
func (m *Manager) DeleteOutput(ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	fileRef, ok := m.outputRefs[ref]
	if !ok {
		return fmt.Errorf("file ref not found: %s", ref)
	}

	if err := os.Remove(fileRef.Path); err != nil && !os.IsNotExist(err) {
		return err
	}

	delete(m.outputRefs, ref)
	return nil
}

// ListOutputs returns all current output refs
func (m *Manager) ListOutputs() []*FileRef {
	m.mu.RLock()
	defer m.mu.RUnlock()

	refs := make([]*FileRef, 0, len(m.outputRefs))
	for _, ref := range m.outputRefs {
		refs = append(refs, ref)
	}
	return refs
}
