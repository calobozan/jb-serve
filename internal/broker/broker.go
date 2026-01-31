// Package broker implements a request broker for distributed jb-serve instances.
//
// The broker aggregates tools from multiple child servers and routes requests
// to the appropriate backend based on tool availability.
package broker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// ChildServer represents a connected jb-serve instance
type ChildServer struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`           // Base URL (e.g., "http://192.168.0.107:9801")
	Name         string    `json:"name"`          // Human-readable name
	Tools        []string  `json:"tools"`         // List of tool names available
	RegisteredAt time.Time `json:"registered_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	Status       string    `json:"status"`        // "healthy", "unhealthy", "dead"
}

// ToolInfo represents aggregated tool information from a child
type ToolInfo struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Version      string   `json:"version"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Status       string   `json:"status"`
	HealthStatus string   `json:"health_status,omitempty"`
	Methods      []string `json:"methods,omitempty"`
	// Broker additions
	ServerID     string   `json:"server_id"`     // Which child server has this tool
	ServerName   string   `json:"server_name"`
}

// Broker manages child server connections and request routing
type Broker struct {
	children map[string]*ChildServer // ID -> ChildServer
	toolMap  map[string]string       // tool name -> child ID
	mu       sync.RWMutex
	client   *http.Client

	// Settings
	heartbeatTimeout time.Duration
	cleanupInterval  time.Duration
	stopCh           chan struct{}
	wg               sync.WaitGroup
}

// New creates a new broker
func New() *Broker {
	b := &Broker{
		children: make(map[string]*ChildServer),
		toolMap:  make(map[string]string),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		heartbeatTimeout: 60 * time.Second,
		cleanupInterval:  30 * time.Second,
		stopCh:           make(chan struct{}),
	}

	// Start cleanup goroutine
	b.wg.Add(1)
	go b.cleanupLoop()

	return b
}

// Register adds or updates a child server
func (b *Broker) Register(child *ChildServer) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	child.RegisteredAt = now
	child.LastHeartbeat = now
	child.Status = "healthy"

	b.children[child.ID] = child

	// Update tool mapping
	for _, tool := range child.Tools {
		b.toolMap[tool] = child.ID
	}

	log.Printf("Registered child server: %s (%s) with %d tools", child.Name, child.URL, len(child.Tools))
	return nil
}

// Heartbeat updates the last heartbeat time for a child
func (b *Broker) Heartbeat(childID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	child, ok := b.children[childID]
	if !ok {
		return fmt.Errorf("unknown child: %s", childID)
	}

	child.LastHeartbeat = time.Now()
	child.Status = "healthy"
	return nil
}

// Unregister removes a child server
func (b *Broker) Unregister(childID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	child, ok := b.children[childID]
	if !ok {
		return
	}

	// Remove tool mappings
	for _, tool := range child.Tools {
		if b.toolMap[tool] == childID {
			delete(b.toolMap, tool)
		}
	}

	delete(b.children, childID)
	log.Printf("Unregistered child server: %s", childID)
}

// GetChild returns a child server by ID
func (b *Broker) GetChild(childID string) (*ChildServer, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	child, ok := b.children[childID]
	return child, ok
}

// GetChildForTool returns the child server that has a specific tool
func (b *Broker) GetChildForTool(toolName string) (*ChildServer, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	childID, ok := b.toolMap[toolName]
	if !ok {
		return nil, false
	}

	child, ok := b.children[childID]
	if !ok {
		return nil, false
	}

	if child.Status != "healthy" {
		return nil, false
	}

	return child, true
}

// ListChildren returns all registered children
func (b *Broker) ListChildren() []*ChildServer {
	b.mu.RLock()
	defer b.mu.RUnlock()

	children := make([]*ChildServer, 0, len(b.children))
	for _, child := range b.children {
		children = append(children, child)
	}
	return children
}

// ListTools aggregates tools from all healthy children
func (b *Broker) ListTools() ([]ToolInfo, error) {
	b.mu.RLock()
	children := make([]*ChildServer, 0, len(b.children))
	for _, child := range b.children {
		if child.Status == "healthy" {
			children = append(children, child)
		}
	}
	b.mu.RUnlock()

	var allTools []ToolInfo

	for _, child := range children {
		tools, err := b.fetchToolsFromChild(child)
		if err != nil {
			log.Printf("Failed to fetch tools from %s: %v", child.Name, err)
			continue
		}

		// Add server info to each tool
		for i := range tools {
			tools[i].ServerID = child.ID
			tools[i].ServerName = child.Name
		}

		allTools = append(allTools, tools...)
	}

	return allTools, nil
}

// fetchToolsFromChild gets the tool list from a child server
func (b *Broker) fetchToolsFromChild(child *ChildServer) ([]ToolInfo, error) {
	resp, err := b.client.Get(child.URL + "/v1/tools")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("child returned status %d", resp.StatusCode)
	}

	var tools []ToolInfo
	if err := json.NewDecoder(resp.Body).Decode(&tools); err != nil {
		return nil, err
	}

	return tools, nil
}

// ProxyRequest forwards a request to the appropriate child server
func (b *Broker) ProxyRequest(w http.ResponseWriter, r *http.Request, toolName string) {
	child, ok := b.GetChildForTool(toolName)
	if !ok {
		http.Error(w, fmt.Sprintf("No server available for tool: %s", toolName), http.StatusServiceUnavailable)
		return
	}

	// Build target URL
	targetURL := child.URL + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// Read request body
	var bodyBytes []byte
	if r.Body != nil {
		bodyBytes, _ = io.ReadAll(r.Body)
	}

	// Create proxied request
	proxyReq, err := http.NewRequest(r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		http.Error(w, "Failed to create proxy request", http.StatusInternalServerError)
		return
	}

	// Copy headers
	for key, values := range r.Header {
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}

	// Add broker headers
	proxyReq.Header.Set("X-Forwarded-For", r.RemoteAddr)
	proxyReq.Header.Set("X-Broker-Request", "true")

	// Execute request
	resp, err := b.client.Do(proxyReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to reach child server: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Copy status and body
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// cleanupLoop removes dead children
func (b *Broker) cleanupLoop() {
	defer b.wg.Done()

	ticker := time.NewTicker(b.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			b.cleanupDeadChildren()
		case <-b.stopCh:
			return
		}
	}
}

// cleanupDeadChildren marks or removes children that haven't sent heartbeats
func (b *Broker) cleanupDeadChildren() {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	for id, child := range b.children {
		if now.Sub(child.LastHeartbeat) > b.heartbeatTimeout {
			if child.Status == "healthy" {
				child.Status = "unhealthy"
				log.Printf("Child server %s marked unhealthy (no heartbeat)", child.Name)
			} else if now.Sub(child.LastHeartbeat) > b.heartbeatTimeout*3 {
				// Remove after 3x timeout
				for _, tool := range child.Tools {
					if b.toolMap[tool] == id {
						delete(b.toolMap, tool)
					}
				}
				delete(b.children, id)
				log.Printf("Child server %s removed (dead)", child.Name)
			}
		}
	}
}

// Close shuts down the broker
func (b *Broker) Close() {
	close(b.stopCh)
	b.wg.Wait()
}
