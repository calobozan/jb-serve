package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/richinsley/jumpboot"
)

// Executor handles RPC calls to tools using jumpboot
type Executor struct {
	manager       *Manager
	repls         map[string]*jumpboot.REPLPythonProcess
	healthCancels map[string]context.CancelFunc
	mu            sync.RWMutex
}

// NewExecutor creates a new executor
func NewExecutor(manager *Manager) *Executor {
	return &Executor{
		manager:       manager,
		repls:         make(map[string]*jumpboot.REPLPythonProcess),
		healthCancels: make(map[string]context.CancelFunc),
	}
}

// Call executes a method on a tool
func (e *Executor) Call(toolName, methodName string, params map[string]interface{}) (map[string]interface{}, error) {
	tool, ok := e.manager.Get(toolName)
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", toolName)
	}

	// Validate method exists
	method, ok := tool.Manifest.RPC.Methods[methodName]
	if !ok {
		return nil, fmt.Errorf("method not found: %s", methodName)
	}
	_ = method // Could validate params against schema

	// Ensure environment is ready
	if err := e.manager.EnsureEnvironment(tool); err != nil {
		return nil, fmt.Errorf("failed to ensure environment: %w", err)
	}

	if tool.Manifest.Runtime.Mode == "persistent" {
		return e.callPersistent(tool, methodName, params)
	}
	return e.callOneshot(tool, methodName, params)
}

// callOneshot runs a tool for a single call using JSONQueue
func (e *Executor) callOneshot(tool *Tool, methodName string, params map[string]interface{}) (map[string]interface{}, error) {
	entrypoint := filepath.Join(tool.Path, tool.Manifest.Runtime.Entrypoint)

	// Create module from tool's entrypoint
	mod, err := jumpboot.NewModuleFromPath(tool.Name, entrypoint)
	if err != nil {
		return nil, fmt.Errorf("failed to load module: %w", err)
	}

	// Create REPL process with JSONQueue
	repl, err := tool.Env.NewREPLPythonProcess(nil, nil, []jumpboot.Module{*mod}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create REPL: %w", err)
	}
	defer repl.Close()

	// Import the tool module
	_, err = repl.Execute(fmt.Sprintf("import %s", tool.Name), true)
	if err != nil {
		return nil, fmt.Errorf("failed to import tool: %w", err)
	}

	// Build the call - use JSONQueue for structured communication
	paramsJSON, _ := json.Marshal(params)
	callExpr := fmt.Sprintf("%s.%s(**%s)", tool.Name, methodName, string(paramsJSON))

	result, err := repl.Execute(callExpr, true)
	if err != nil {
		return nil, fmt.Errorf("call failed: %w", err)
	}

	// Parse result - handle potential string quoting from REPL
	resultStr := result
	// Strip outer quotes if present (REPL returns quoted strings)
	if len(resultStr) >= 2 && resultStr[0] == '\'' && resultStr[len(resultStr)-1] == '\'' {
		resultStr = resultStr[1 : len(resultStr)-1]
	}
	
	var response map[string]interface{}
	if err := json.Unmarshal([]byte(resultStr), &response); err != nil {
		// If not JSON, wrap in result
		return map[string]interface{}{"result": result}, nil
	}

	return response, nil
}

// callPersistent calls a method on a running persistent tool
func (e *Executor) callPersistent(tool *Tool, methodName string, params map[string]interface{}) (map[string]interface{}, error) {
	e.mu.RLock()
	repl, ok := e.repls[tool.Name]
	e.mu.RUnlock()

	if !ok || repl == nil {
		return nil, fmt.Errorf("tool %s is not running, start it first", tool.Name)
	}

	// Build the call
	paramsJSON, _ := json.Marshal(params)
	callExpr := fmt.Sprintf("%s.%s(**%s)", tool.Name, methodName, string(paramsJSON))

	result, err := repl.Execute(callExpr, true)
	if err != nil {
		return nil, fmt.Errorf("call failed: %w", err)
	}

	// Strip outer quotes if present
	resultStr := result
	if len(resultStr) >= 2 && resultStr[0] == '\'' && resultStr[len(resultStr)-1] == '\'' {
		resultStr = resultStr[1 : len(resultStr)-1]
	}
	
	var response map[string]interface{}
	if err := json.Unmarshal([]byte(resultStr), &response); err != nil {
		return map[string]interface{}{"result": result}, nil
	}

	return response, nil
}

// Start starts a persistent tool
func (e *Executor) Start(toolName string) error {
	tool, ok := e.manager.Get(toolName)
	if !ok {
		return fmt.Errorf("tool not found: %s", toolName)
	}

	if tool.Manifest.Runtime.Mode != "persistent" {
		return fmt.Errorf("tool %s is not a persistent tool", toolName)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if _, ok := e.repls[toolName]; ok {
		return fmt.Errorf("tool %s is already running", toolName)
	}

	// Ensure environment
	if err := e.manager.EnsureEnvironment(tool); err != nil {
		return err
	}

	entrypoint := filepath.Join(tool.Path, tool.Manifest.Runtime.Entrypoint)
	mod, err := jumpboot.NewModuleFromPath(tool.Name, entrypoint)
	if err != nil {
		return fmt.Errorf("failed to load module: %w", err)
	}

	repl, err := tool.Env.NewREPLPythonProcess(nil, nil, []jumpboot.Module{*mod}, nil)
	if err != nil {
		return fmt.Errorf("failed to create REPL: %w", err)
	}

	// Import the tool module
	_, err = repl.Execute(fmt.Sprintf("import %s", tool.Name), true)
	if err != nil {
		repl.Close()
		return fmt.Errorf("failed to import tool: %w", err)
	}

	e.repls[toolName] = repl
	tool.Status = "running"
	tool.HealthStatus = "unknown"
	tool.HealthFailures = 0

	// Start health check if configured
	if tool.Manifest.Health != nil {
		ctx, cancel := context.WithCancel(context.Background())
		e.healthCancels[toolName] = cancel
		go e.runHealthCheck(ctx, tool)
	}

	fmt.Printf("Started %s\n", toolName)
	return nil
}

// Stop stops a persistent tool
func (e *Executor) Stop(toolName string) error {
	tool, ok := e.manager.Get(toolName)
	if !ok {
		return fmt.Errorf("tool not found: %s", toolName)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	repl, ok := e.repls[toolName]
	if !ok {
		return fmt.Errorf("tool %s is not running", toolName)
	}

	// Cancel health check if running
	if cancel, ok := e.healthCancels[toolName]; ok {
		cancel()
		delete(e.healthCancels, toolName)
	}

	repl.Close()
	delete(e.repls, toolName)
	tool.Status = "stopped"
	tool.HealthStatus = ""
	tool.HealthFailures = 0

	fmt.Printf("Stopped %s\n", toolName)
	return nil
}

// Close stops all running tools
func (e *Executor) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Cancel all health checks
	for _, cancel := range e.healthCancels {
		cancel()
	}
	e.healthCancels = make(map[string]context.CancelFunc)

	// Close all REPLs
	for name, repl := range e.repls {
		repl.Close()
		if tool, ok := e.manager.Get(name); ok {
			tool.Status = "stopped"
			tool.HealthStatus = ""
		}
	}
	e.repls = make(map[string]*jumpboot.REPLPythonProcess)
}

// runHealthCheck runs periodic health checks for a tool
func (e *Executor) runHealthCheck(ctx context.Context, tool *Tool) {
	healthCfg := tool.Manifest.Health
	interval := time.Duration(healthCfg.Interval) * time.Second
	method := healthCfg.Method
	threshold := healthCfg.FailureThreshold

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial health check after a short delay
	time.Sleep(2 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := e.doHealthCheck(tool, method)
			if err != nil {
				tool.HealthFailures++
				if tool.HealthFailures >= threshold {
					if tool.HealthStatus != "unhealthy" {
						tool.HealthStatus = "unhealthy"
						log.Printf("Health check failed for %s: %v (failures: %d)", tool.Name, err, tool.HealthFailures)
					}
				}
			} else {
				if tool.HealthStatus != "healthy" {
					log.Printf("Health check passed for %s", tool.Name)
				}
				tool.HealthStatus = "healthy"
				tool.HealthFailures = 0
			}
		}
	}
}

// doHealthCheck performs a single health check call
func (e *Executor) doHealthCheck(tool *Tool, method string) error {
	e.mu.RLock()
	repl, ok := e.repls[tool.Name]
	e.mu.RUnlock()

	if !ok || repl == nil {
		return fmt.Errorf("tool not running")
	}

	// Call the health method
	callExpr := fmt.Sprintf("%s.%s()", tool.Name, method)
	result, err := repl.Execute(callExpr, true)
	if err != nil {
		return fmt.Errorf("health call failed: %w", err)
	}

	// Parse result and check status
	resultStr := result
	if len(resultStr) >= 2 && resultStr[0] == '\'' && resultStr[len(resultStr)-1] == '\'' {
		resultStr = resultStr[1 : len(resultStr)-1]
	}

	var response map[string]interface{}
	if err := json.Unmarshal([]byte(resultStr), &response); err != nil {
		return fmt.Errorf("invalid health response: %w", err)
	}

	// Check for "ok" status
	if status, ok := response["status"].(string); ok && status == "ok" {
		return nil
	}

	return fmt.Errorf("unhealthy status: %v", response)
}
