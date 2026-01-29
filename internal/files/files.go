package files

import (
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// Manager handles temporary file storage for uploads
type Manager struct {
	uploadDir string
}

// NewManager creates a file manager with the given upload directory
func NewManager(baseDir string) (*Manager, error) {
	uploadDir := filepath.Join(baseDir, "uploads")
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create upload dir: %w", err)
	}
	return &Manager{uploadDir: uploadDir}, nil
}

// SaveUpload saves a multipart file to temp storage and returns the path
func (m *Manager) SaveUpload(file multipart.File, header *multipart.FileHeader) (string, error) {
	// Generate unique filename preserving extension
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

// Cleanup removes a temporary file
func (m *Manager) Cleanup(path string) error {
	// Only delete files in our upload directory
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
