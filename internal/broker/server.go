package broker

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// Server is the HTTP server for the broker
type Server struct {
	broker *Broker
	mux    *http.ServeMux
}

// NewServer creates a new broker HTTP server
func NewServer() *Server {
	s := &Server{
		broker: New(),
		mux:    http.NewServeMux(),
	}
	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	// Broker management endpoints
	s.mux.HandleFunc("/v1/broker/register", s.handleRegister)
	s.mux.HandleFunc("/v1/broker/heartbeat", s.handleHeartbeat)
	s.mux.HandleFunc("/v1/broker/children", s.handleChildren)

	// Aggregated endpoints
	s.mux.HandleFunc("/v1/tools", s.handleTools)
	s.mux.HandleFunc("/v1/tools/", s.handleToolProxy)

	// File store proxy (routes to appropriate child)
	s.mux.HandleFunc("/v1/store", s.handleStoreProxy)
	s.mux.HandleFunc("/v1/store/", s.handleStoreProxy)

	// Health
	s.mux.HandleFunc("/health", s.handleHealth)
}

// ListenAndServe starts the broker server
func (s *Server) ListenAndServe(port int) error {
	addr := fmt.Sprintf(":%d", port)
	log.Printf("jb-serve broker listening on %s", addr)
	return http.ListenAndServe(addr, s.mux)
}

// Close shuts down the broker
func (s *Server) Close() {
	s.broker.Close()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	children := s.broker.ListChildren()
	healthy := 0
	for _, c := range children {
		if c.Status == "healthy" {
			healthy++
		}
	}

	s.json(w, map[string]interface{}{
		"status":          "ok",
		"mode":            "broker",
		"children_total":  len(children),
		"children_healthy": healthy,
	})
}

// handleRegister handles child server registration
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID    string   `json:"id"`
		URL   string   `json:"url"`
		Name  string   `json:"name"`
		Tools []string `json:"tools"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.ID == "" || req.URL == "" {
		s.jsonError(w, "id and url are required", http.StatusBadRequest)
		return
	}

	child := &ChildServer{
		ID:    req.ID,
		URL:   req.URL,
		Name:  req.Name,
		Tools: req.Tools,
	}

	if child.Name == "" {
		child.Name = child.ID
	}

	if err := s.broker.Register(child); err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.json(w, map[string]interface{}{
		"status":     "registered",
		"id":         child.ID,
		"heartbeat_interval": int(s.broker.heartbeatTimeout.Seconds() / 2),
	})
}

// handleHeartbeat handles child heartbeats
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID    string   `json:"id"`
		Tools []string `json:"tools,omitempty"` // Optional: update tool list
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.ID == "" {
		s.jsonError(w, "id is required", http.StatusBadRequest)
		return
	}

	// If tools provided, re-register (updates tool list)
	if len(req.Tools) > 0 {
		child, ok := s.broker.GetChild(req.ID)
		if ok {
			child.Tools = req.Tools
			s.broker.Register(child)
		}
	}

	if err := s.broker.Heartbeat(req.ID); err != nil {
		s.jsonError(w, err.Error(), http.StatusNotFound)
		return
	}

	s.json(w, map[string]string{"status": "ok"})
}

// handleChildren lists registered children
func (s *Server) handleChildren(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	children := s.broker.ListChildren()
	s.json(w, children)
}

// handleTools aggregates tools from all children
func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tools, err := s.broker.ListTools()
	if err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.json(w, tools)
}

// handleToolProxy proxies tool requests to the appropriate child
func (s *Server) handleToolProxy(w http.ResponseWriter, r *http.Request) {
	// Extract tool name from path: /v1/tools/{tool}/...
	path := strings.TrimPrefix(r.URL.Path, "/v1/tools/")
	parts := strings.SplitN(path, "/", 2)
	toolName := parts[0]

	if toolName == "" {
		http.Error(w, "Tool name required", http.StatusBadRequest)
		return
	}

	s.broker.ProxyRequest(w, r, toolName)
}

// handleStoreProxy proxies file store requests
// For now, we need to know which child to route to
// This could be enhanced to route based on file ID prefix or a central store
func (s *Server) handleStoreProxy(w http.ResponseWriter, r *http.Request) {
	// For file store, we could:
	// 1. Route to a specific "primary" child
	// 2. Route based on file ID (if IDs encode server info)
	// 3. Broadcast to all and return first match
	// For now, return not implemented
	s.jsonError(w, "File store proxy not yet implemented - access child servers directly", http.StatusNotImplemented)
}

func (s *Server) json(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *Server) jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
