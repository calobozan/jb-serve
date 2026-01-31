package broker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ChildClient handles registration with a broker
type ChildClient struct {
	brokerURL string
	selfURL   string
	id        string
	name      string
	tools     []string

	client   *http.Client
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup
	mu       sync.RWMutex
}

// NewChildClient creates a client for registering with a broker
func NewChildClient(brokerURL, selfURL, name string) *ChildClient {
	hostname, _ := os.Hostname()
	if name == "" {
		name = hostname
	}

	return &ChildClient{
		brokerURL: brokerURL,
		selfURL:   selfURL,
		id:        uuid.New().String()[:8] + "-" + hostname,
		name:      name,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		interval: 30 * time.Second,
		stopCh:   make(chan struct{}),
	}
}

// SetTools updates the list of tools to report
func (c *ChildClient) SetTools(tools []string) {
	c.mu.Lock()
	c.tools = tools
	c.mu.Unlock()
}

// Register connects to the broker and starts heartbeat
func (c *ChildClient) Register() error {
	c.mu.RLock()
	tools := c.tools
	c.mu.RUnlock()

	req := map[string]interface{}{
		"id":    c.id,
		"url":   c.selfURL,
		"name":  c.name,
		"tools": tools,
	}

	body, _ := json.Marshal(req)
	resp, err := c.client.Post(c.brokerURL+"/v1/broker/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to register with broker: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("broker registration failed: %s", errResp["error"])
	}

	var result struct {
		Status            string `json:"status"`
		HeartbeatInterval int    `json:"heartbeat_interval"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if result.HeartbeatInterval > 0 {
		c.interval = time.Duration(result.HeartbeatInterval) * time.Second
	}

	log.Printf("Registered with broker %s (heartbeat every %v)", c.brokerURL, c.interval)

	// Start heartbeat goroutine
	c.wg.Add(1)
	go c.heartbeatLoop()

	return nil
}

// heartbeatLoop sends periodic heartbeats to the broker
func (c *ChildClient) heartbeatLoop() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := c.sendHeartbeat(); err != nil {
				log.Printf("Heartbeat failed: %v", err)
				// Try to re-register
				if err := c.Register(); err != nil {
					log.Printf("Re-registration failed: %v", err)
				}
			}
		case <-c.stopCh:
			return
		}
	}
}

// sendHeartbeat sends a single heartbeat
func (c *ChildClient) sendHeartbeat() error {
	c.mu.RLock()
	tools := c.tools
	c.mu.RUnlock()

	req := map[string]interface{}{
		"id":    c.id,
		"tools": tools,
	}

	body, _ := json.Marshal(req)
	resp, err := c.client.Post(c.brokerURL+"/v1/broker/heartbeat", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("heartbeat returned %d", resp.StatusCode)
	}

	return nil
}

// Stop stops the heartbeat and unregisters
func (c *ChildClient) Stop() {
	close(c.stopCh)
	c.wg.Wait()
}

// ID returns the child's ID
func (c *ChildClient) ID() string {
	return c.id
}
