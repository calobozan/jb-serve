package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/calobozan/jb-serve/internal/config"
	"github.com/calobozan/jb-serve/internal/tools"
)

// Server is the jb-serve HTTP API server
type Server struct {
	cfg      *config.Config
	manager  *tools.Manager
	executor *tools.Executor
	mux      *http.ServeMux
}

// New creates a new API server
func New(cfg *config.Config, manager *tools.Manager, executor *tools.Executor) *Server {
	s := &Server{
		cfg:      cfg,
		manager:  manager,
		executor: executor,
		mux:      http.NewServeMux(),
	}
	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	s.mux.HandleFunc("/v1/tools", s.handleTools)
	s.mux.HandleFunc("/v1/tools/", s.handleTool)
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
		_, ok := tool.Manifest.RPC.Methods[action]
		if !ok {
			http.Error(w, "Method not found", http.StatusNotFound)
			return
		}

		var params map[string]interface{}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
				http.Error(w, "Invalid JSON", http.StatusBadRequest)
				return
			}
		}

		result, err := s.executor.Call(toolName, action, params)
		if err != nil {
			s.json(w, map[string]string{"error": err.Error()})
			return
		}

		s.json(w, result)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) json(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
