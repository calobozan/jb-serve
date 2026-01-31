package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/calobozan/jb-serve/internal/config"
	"github.com/calobozan/jb-serve/internal/files"
	"github.com/calobozan/jb-serve/internal/filestore"
	"github.com/calobozan/jb-serve/internal/tools"
)

// Server is the jb-serve HTTP API server
type Server struct {
	cfg       *config.Config
	manager   *tools.Manager
	executor  *tools.Executor
	files     *files.Manager
	filestore *filestore.Store
	mux       *http.ServeMux
}

// Options configures the server
type Options struct {
	FileStorePath    string // Custom path for file store (empty = use base dir)
	FileStoreDisable bool   // Disable file store entirely
}

// New creates a new API server with default options
func New(cfg *config.Config, manager *tools.Manager, executor *tools.Executor) *Server {
	return NewWithOptions(cfg, manager, executor, Options{})
}

// NewWithOptions creates a new API server with custom options
func NewWithOptions(cfg *config.Config, manager *tools.Manager, executor *tools.Executor, opts Options) *Server {
	fileMgr, err := files.NewManager(cfg.BaseDir())
	if err != nil {
		log.Printf("Warning: failed to create file manager: %v", err)
	}

	// Create persistent file store (unless disabled)
	var store *filestore.Store
	if !opts.FileStoreDisable {
		storePath := opts.FileStorePath
		if storePath == "" {
			storePath = cfg.BaseDir()
		}
		store, err = filestore.New(storePath)
		if err != nil {
			log.Printf("Warning: failed to create filestore at %s: %v", storePath, err)
		} else {
			log.Printf("File store initialized at %s", storePath)
		}
	} else {
		log.Printf("File store disabled")
	}

	s := &Server{
		cfg:       cfg,
		manager:   manager,
		executor:  executor,
		files:     fileMgr,
		filestore: store,
		mux:       http.NewServeMux(),
	}
	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	s.mux.HandleFunc("/v1/tools", s.handleTools)
	s.mux.HandleFunc("/v1/tools/", s.handleTool)
	s.mux.HandleFunc("/v1/files/", s.handleFiles)
	s.mux.HandleFunc("/v1/store", s.handleStore)
	s.mux.HandleFunc("/v1/store/", s.handleStoreItem)
	s.mux.HandleFunc("/health", s.handleHealth)
}

// ListenAndServe starts the server
func (s *Server) ListenAndServe(port int) error {
	addr := fmt.Sprintf(":%d", port)
	log.Printf("jb-serve API listening on %s", addr)
	return http.ListenAndServe(addr, s.authMiddleware(s.mux))
}

// Close cleans up server resources
func (s *Server) Close() error {
	if s.filestore != nil {
		return s.filestore.Close()
	}
	return nil
}

// FileStore returns the server's file store for external access
func (s *Server) FileStore() *filestore.Store {
	return s.filestore
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AuthToken != "" {
			token := r.Header.Get("Authorization")
			if token == "" {
				token = r.URL.Query().Get("token")
			}
			expected := "Bearer " + s.cfg.AuthToken
			if token != expected && token != s.cfg.AuthToken {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.json(w, map[string]string{"status": "ok"})
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	type toolSummary struct {
		Name         string   `json:"name"`
		Version      string   `json:"version"`
		Description  string   `json:"description"`
		Capabilities []string `json:"capabilities"`
		Mode         string   `json:"mode"`
		Status       string   `json:"status"`
		HealthStatus string   `json:"health_status,omitempty"`
		Methods      []string `json:"methods"`
	}

	toolList := s.manager.List()
	summaries := make([]toolSummary, len(toolList))

	for i, t := range toolList {
		methods := make([]string, 0, len(t.Manifest.RPC.Methods))
		for name := range t.Manifest.RPC.Methods {
			methods = append(methods, name)
		}

		summaries[i] = toolSummary{
			Name:         t.Name,
			Version:      t.Manifest.Version,
			Description:  t.Manifest.Description,
			Capabilities: t.Manifest.Capabilities,
			Mode:         t.Manifest.Runtime.Mode,
			Status:       t.Status,
			HealthStatus: t.HealthStatus,
			Methods:      methods,
		}
	}

	s.json(w, summaries)
}

func (s *Server) handleTool(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/tools/")
	parts := strings.SplitN(path, "/", 2)

	toolName := parts[0]
	tool, ok := s.manager.Get(toolName)
	if !ok {
		http.Error(w, "Tool not found", http.StatusNotFound)
		return
	}

	// GET /v1/tools/{name}
	if len(parts) == 1 || parts[1] == "" {
		if r.Method == http.MethodGet {
			info, _ := s.manager.Info(toolName)
			s.json(w, info)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	action := parts[1]

	// GET /v1/tools/{name}/schema
	if action == "schema" && r.Method == http.MethodGet {
		s.json(w, tool.Manifest.RPC.Methods)
		return
	}

	// POST /v1/tools/{name}/start - start a persistent tool
	if action == "start" && r.Method == http.MethodPost {
		if tool.Manifest.Runtime.Mode != "persistent" {
			s.json(w, map[string]string{"error": "tool is not a persistent tool"})
			return
		}
		if err := s.executor.Start(toolName); err != nil {
			s.json(w, map[string]string{"error": err.Error()})
			return
		}
		s.json(w, map[string]string{"status": "started", "tool": toolName})
		return
	}

	// POST /v1/tools/{name}/stop - stop a persistent tool
	if action == "stop" && r.Method == http.MethodPost {
		if err := s.executor.Stop(toolName); err != nil {
			s.json(w, map[string]string{"error": err.Error()})
			return
		}
		s.json(w, map[string]string{"status": "stopped", "tool": toolName})
		return
	}

	// POST /v1/tools/{name}/{method} - call a method
	if r.Method == http.MethodPost {
		method, ok := tool.Manifest.RPC.Methods[action]
		if !ok {
			http.Error(w, "Method not found", http.StatusNotFound)
			return
		}

		params, tempFiles, err := s.parseRequestParams(r, method)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Ensure temp files are cleaned up after the call
		if len(tempFiles) > 0 && s.files != nil {
			defer s.files.CleanupAll(tempFiles)
		}

		result, err := s.executor.Call(toolName, action, params)
		if err != nil {
			s.json(w, map[string]string{"error": err.Error()})
			return
		}

		// Wrap file outputs with refs
		wrappedResult := s.wrapFileOutputs(result, method)

		s.json(w, wrappedResult)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) json(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// wrapFileOutputs walks through a result and converts file paths to FileRefs
// It looks for string values that are valid file paths and converts them
func (s *Server) wrapFileOutputs(result interface{}, method config.Method) interface{} {
	if s.files == nil {
		return result
	}

	// Get output file fields from schema
	fileFields := getOutputFileFields(method)

	switch v := result.(type) {
	case map[string]interface{}:
		wrapped := make(map[string]interface{})
		for key, val := range v {
			// Check if this field is marked as type: file in schema
			if fileFields[key] {
				if path, ok := val.(string); ok && isFilePath(path) {
					if ref, err := s.files.RegisterOutput(path); err == nil {
						wrapped[key] = ref
						continue
					}
				}
			}
			// Recursively wrap nested maps
			wrapped[key] = s.wrapFileOutputs(val, method)
		}
		return wrapped
	case []interface{}:
		wrapped := make([]interface{}, len(v))
		for i, item := range v {
			wrapped[i] = s.wrapFileOutputs(item, method)
		}
		return wrapped
	default:
		return result
	}
}

// isFilePath checks if a string looks like a file path that exists
func isFilePath(s string) bool {
	if !strings.HasPrefix(s, "/") {
		return false
	}
	info, err := os.Stat(s)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// getOutputFileFields returns field names marked as type: file in output schema
func getOutputFileFields(method config.Method) map[string]bool {
	fields := make(map[string]bool)
	if method.Output != nil && method.Output.Properties != nil {
		for name, prop := range method.Output.Properties {
			if prop != nil && prop.Type == "file" {
				fields[name] = true
			}
		}
	}
	return fields
}

// handleFiles serves output files
func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	if s.files == nil {
		http.Error(w, "File serving not configured", http.StatusServiceUnavailable)
		return
	}

	// Extract filename from path: /v1/files/abc123.png -> abc123.png
	filename := strings.TrimPrefix(r.URL.Path, "/v1/files/")
	if filename == "" {
		// List files
		if r.Method == http.MethodGet {
			s.json(w, s.files.ListOutputs())
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Serve file
	if r.Method == http.MethodGet {
		filePath := s.files.GetOutputPath(filename)
		http.ServeFile(w, r, filePath)
		return
	}

	// Delete file
	if r.Method == http.MethodDelete {
		// Extract ref from filename (remove extension)
		ref := strings.TrimSuffix(filename, filepath.Ext(filename))
		if err := s.files.DeleteOutput(ref); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		s.json(w, map[string]string{"status": "deleted"})
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// handleStore handles /v1/store (list, import, stats)
func (s *Server) handleStore(w http.ResponseWriter, r *http.Request) {
	if s.filestore == nil {
		http.Error(w, "File store not configured", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// GET /v1/store - list files
		includeExpired := r.URL.Query().Get("include_expired") == "true"
		files, err := s.filestore.List(includeExpired)
		if err != nil {
			s.jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.json(w, map[string]interface{}{
			"files": files,
		})

	case http.MethodPost:
		// POST /v1/store - import a file
		// Accepts multipart form with file upload, or JSON with path
		contentType := r.Header.Get("Content-Type")

		var sourcePath string
		var name string
		var ttl int64

		if strings.HasPrefix(contentType, "multipart/form-data") {
			// File upload
			if err := r.ParseMultipartForm(256 << 20); err != nil { // 256MB max
				s.jsonError(w, "Failed to parse form: "+err.Error(), http.StatusBadRequest)
				return
			}

			file, header, err := r.FormFile("file")
			if err != nil {
				s.jsonError(w, "No file provided", http.StatusBadRequest)
				return
			}
			defer file.Close()

			// Save to temp location first
			tempPath, err := s.files.SaveUpload(file, header)
			if err != nil {
				s.jsonError(w, "Failed to save upload: "+err.Error(), http.StatusInternalServerError)
				return
			}
			defer os.Remove(tempPath)

			sourcePath = tempPath
			name = r.FormValue("name")
			if name == "" {
				name = header.Filename
			}
			if ttlStr := r.FormValue("ttl"); ttlStr != "" {
				ttl, _ = strconv.ParseInt(ttlStr, 10, 64)
			}
		} else {
			// JSON with path
			var req struct {
				Path string `json:"path"`
				Name string `json:"name"`
				TTL  int64  `json:"ttl"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				s.jsonError(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			if req.Path == "" {
				s.jsonError(w, "path is required", http.StatusBadRequest)
				return
			}
			sourcePath = req.Path
			name = req.Name
			ttl = req.TTL
		}

		info, err := s.filestore.Import(sourcePath, name, ttl)
		if err != nil {
			s.jsonError(w, "Import failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		s.json(w, info)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleStoreItem handles /v1/store/{id} (get, info, rename, delete, content)
func (s *Server) handleStoreItem(w http.ResponseWriter, r *http.Request) {
	if s.filestore == nil {
		http.Error(w, "File store not configured", http.StatusServiceUnavailable)
		return
	}

	// Extract ID from path: /v1/store/{id} or /v1/store/{id}/content
	path := strings.TrimPrefix(r.URL.Path, "/v1/store/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]

	if id == "" {
		http.Error(w, "ID required", http.StatusBadRequest)
		return
	}

	// Check for /content suffix
	isContent := len(parts) > 1 && parts[1] == "content"

	switch r.Method {
	case http.MethodGet:
		if isContent {
			// GET /v1/store/{id}/content - download file
			blobPath, err := s.filestore.GetPath(id)
			if err != nil {
				s.jsonError(w, err.Error(), http.StatusNotFound)
				return
			}
			http.ServeFile(w, r, blobPath)
			return
		}

		// GET /v1/store/{id} - get info
		info, err := s.filestore.Info(id)
		if err != nil {
			s.jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		s.json(w, info)

	case http.MethodPatch:
		// PATCH /v1/store/{id} - rename or set TTL
		var req struct {
			Name string `json:"name,omitempty"`
			TTL  *int64 `json:"ttl,omitempty"` // pointer to distinguish 0 from unset
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.jsonError(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Name != "" {
			if err := s.filestore.Rename(id, req.Name); err != nil {
				s.jsonError(w, err.Error(), http.StatusNotFound)
				return
			}
		}

		if req.TTL != nil {
			if err := s.filestore.SetTTL(id, *req.TTL); err != nil {
				s.jsonError(w, err.Error(), http.StatusNotFound)
				return
			}
		}

		// Return updated info
		info, err := s.filestore.Info(id)
		if err != nil {
			s.jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		s.json(w, info)

	case http.MethodDelete:
		// DELETE /v1/store/{id} - delete file
		if err := s.filestore.Delete(id); err != nil {
			s.jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		s.json(w, map[string]string{"status": "deleted", "id": id})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// parseRequestParams extracts parameters from JSON or multipart form data
// Returns params map, list of temp file paths to clean up, and any error
func (s *Server) parseRequestParams(r *http.Request, method config.Method) (map[string]interface{}, []string, error) {
	params := make(map[string]interface{})
	var tempFiles []string

	contentType := r.Header.Get("Content-Type")

	// Handle multipart form data (file uploads)
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if s.files == nil {
			return nil, nil, fmt.Errorf("file uploads not configured")
		}

		// Parse multipart form (32MB max memory, rest goes to temp files)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return nil, nil, fmt.Errorf("failed to parse multipart form: %w", err)
		}

		// Get file fields from schema
		fileFields := getFileFields(method)

		// Process uploaded files
		for fieldName := range r.MultipartForm.File {
			file, header, err := r.FormFile(fieldName)
			if err != nil {
				continue
			}
			defer file.Close()

			// Save to temp and get path
			path, err := s.files.SaveUpload(file, header)
			if err != nil {
				// Clean up any files we've already saved
				s.files.CleanupAll(tempFiles)
				return nil, nil, fmt.Errorf("failed to save upload %s: %w", fieldName, err)
			}

			tempFiles = append(tempFiles, path)
			params[fieldName] = path
		}

		// Process non-file form values
		for key, values := range r.MultipartForm.Value {
			if len(values) > 0 {
				// Special case: "params" field contains JSON with other parameters
				if key == "params" {
					var jsonParams map[string]interface{}
					if err := json.Unmarshal([]byte(values[0]), &jsonParams); err == nil {
						for k, v := range jsonParams {
							// Don't overwrite file fields
							if _, isFile := fileFields[k]; !isFile {
								params[k] = v
							}
						}
					}
				} else if _, isFile := fileFields[key]; !isFile {
					params[key] = values[0]
				}
			}
		}

		return params, tempFiles, nil
	}

	// Handle JSON (default)
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			return nil, nil, fmt.Errorf("invalid JSON: %w", err)
		}
	}

	return params, nil, nil
}

// getFileFields returns a set of field names that have type "file" in the schema
func getFileFields(method config.Method) map[string]bool {
	fileFields := make(map[string]bool)
	if method.Input != nil && method.Input.Properties != nil {
		for name, prop := range method.Input.Properties {
			if prop != nil && prop.Type == "file" {
				fileFields[name] = true
			}
		}
	}
	return fileFields
}
