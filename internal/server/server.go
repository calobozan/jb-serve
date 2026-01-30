package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/calobozan/jb-serve/internal/config"
	"github.com/calobozan/jb-serve/internal/files"
	"github.com/calobozan/jb-serve/internal/tools"
)

// Server is the jb-serve HTTP API server
type Server struct {
	cfg      *config.Config
	manager  *tools.Manager
	executor *tools.Executor
	files    *files.Manager
	mux      *http.ServeMux
}

// New creates a new API server
func New(cfg *config.Config, manager *tools.Manager, executor *tools.Executor) *Server {
	fileMgr, err := files.NewManager(cfg.BaseDir())
	if err != nil {
		log.Printf("Warning: failed to create file manager: %v", err)
	}

	s := &Server{
		cfg:      cfg,
		manager:  manager,
		executor: executor,
		files:    fileMgr,
		mux:      http.NewServeMux(),
	}
	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	s.mux.HandleFunc("/v1/tools", s.handleTools)
	s.mux.HandleFunc("/v1/tools/", s.handleTool)
	s.mux.HandleFunc("/v1/files/", s.handleFiles)
	s.mux.HandleFunc("/health", s.handleHealth)
}

// ListenAndServe starts the server
func (s *Server) ListenAndServe(port int) error {
	addr := fmt.Sprintf(":%d", port)
	log.Printf("jb-serve API listening on %s", addr)
	return http.ListenAndServe(addr, s.authMiddleware(s.mux))
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
