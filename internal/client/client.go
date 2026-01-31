// Package client provides an HTTP client for the jb-serve API.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is an HTTP client for the jb-serve API.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// New creates a new client for the given server URL.
func New(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Minute, // Long timeout for model loading
		},
	}
}

// NewFromPort creates a client for localhost on the given port.
func NewFromPort(port int) *Client {
	return New(fmt.Sprintf("http://localhost:%d", port))
}

// ToolInfo represents tool information from the API.
// Note: Methods field differs between list ([]string) and info (map) endpoints.
type ToolInfo struct {
	Name         string      `json:"name"`
	Version      string      `json:"version"`
	Description  string      `json:"description"`
	Capabilities []string    `json:"capabilities"`
	Mode         string      `json:"mode"`
	Status       string      `json:"status"`
	HealthStatus string      `json:"health_status,omitempty"`
	Methods      interface{} `json:"methods,omitempty"` // []string for list, map for info
}

// MethodNames returns method names as a slice (works for both list and info responses).
func (t *ToolInfo) MethodNames() []string {
	switch m := t.Methods.(type) {
	case []interface{}:
		names := make([]string, len(m))
		for i, v := range m {
			names[i] = fmt.Sprintf("%v", v)
		}
		return names
	case map[string]interface{}:
		names := make([]string, 0, len(m))
		for k := range m {
			names = append(names, k)
		}
		return names
	default:
		return nil
	}
}

// StatusResponse is the response from start/stop operations.
type StatusResponse struct {
	Status string `json:"status"`
	Tool   string `json:"tool"`
	Error  string `json:"error,omitempty"`
}

// Ping checks if the server is reachable.
func (c *Client) Ping() error {
	resp, err := c.HTTPClient.Get(c.BaseURL + "/health")
	if err != nil {
		return fmt.Errorf("server not reachable: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned status %d", resp.StatusCode)
	}
	return nil
}

// List returns all installed tools.
func (c *Client) List() ([]ToolInfo, error) {
	resp, err := c.HTTPClient.Get(c.BaseURL + "/v1/tools")
	if err != nil {
		return nil, fmt.Errorf("failed to list tools: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server error: %s", string(body))
	}

	var tools []ToolInfo
	if err := json.NewDecoder(resp.Body).Decode(&tools); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return tools, nil
}

// Info returns details for a specific tool.
func (c *Client) Info(toolName string) (*ToolInfo, error) {
	resp, err := c.HTTPClient.Get(c.BaseURL + "/v1/tools/" + toolName)
	if err != nil {
		return nil, fmt.Errorf("failed to get tool info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("tool not found: %s", toolName)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server error: %s", string(body))
	}

	var info ToolInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &info, nil
}

// Start starts a persistent tool.
func (c *Client) Start(toolName string) (*StatusResponse, error) {
	resp, err := c.HTTPClient.Post(c.BaseURL+"/v1/tools/"+toolName+"/start", "application/json", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start tool: %w", err)
	}
	defer resp.Body.Close()

	var status StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	
	if status.Error != "" {
		return nil, fmt.Errorf("failed to start tool: %s", status.Error)
	}
	return &status, nil
}

// Stop stops a persistent tool.
func (c *Client) Stop(toolName string) (*StatusResponse, error) {
	resp, err := c.HTTPClient.Post(c.BaseURL+"/v1/tools/"+toolName+"/stop", "application/json", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to stop tool: %w", err)
	}
	defer resp.Body.Close()

	var status StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	
	if status.Error != "" {
		return nil, fmt.Errorf("failed to stop tool: %s", status.Error)
	}
	return &status, nil
}

// Call invokes a method on a tool.
func (c *Client) Call(toolName, methodName string, params map[string]interface{}) (map[string]interface{}, error) {
	var body io.Reader
	if params != nil && len(params) > 0 {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("failed to encode params: %w", err)
		}
		body = bytes.NewReader(data)
	}

	url := fmt.Sprintf("%s/v1/tools/%s/%s", c.BaseURL, toolName, methodName)
	resp, err := c.HTTPClient.Post(url, "application/json", body)
	if err != nil {
		return nil, fmt.Errorf("failed to call method: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("method call failed: %s", string(body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return result, nil
}
