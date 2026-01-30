package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/richinsley/jumpboot"
)

// CallResponse represents the response from a jb-service call
type CallResponse struct {
	OK     bool                   `json:"ok"`
	Result interface{}            `json:"result,omitempty"`
	Error  *CallError             `json:"error,omitempty"`
	Done   bool                   `json:"done"`
	Chunk  interface{}            `json:"chunk,omitempty"` // For future streaming support
}

// CallError represents an error from a jb-service call
type CallError struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	Traceback string `json:"traceback,omitempty"`
}

// Executor handles RPC calls to tools using jumpboot
type Executor struct {
	manager       *Manager
	repls         map[string]*jumpboot.REPLPythonProcess  // REPL transport
	queues        map[string]*jumpboot.QueueProcess       // MessagePack transport
	healthCancels map[string]context.CancelFunc
	mu            sync.RWMutex
}

// NewExecutor creates a new executor
func NewExecutor(manager *Manager) *Executor {
	return &Executor{
		manager:       manager,
		repls:         make(map[string]*jumpboot.REPLPythonProcess),
		queues:        make(map[string]*jumpboot.QueueProcess),
		healthCancels: make(map[string]context.CancelFunc),
	}
}

// Call executes a method on a tool
func (e *Executor) Call(toolName, methodName string, params map[string]interface{}) (interface{}, error) {
	tool, ok := e.manager.Get(toolName)
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", toolName)
	}

	// Validate method exists in manifest
	method, ok := tool.Manifest.RPC.Methods[methodName]
	if !ok {
		return nil, fmt.Errorf("method not found: %s", methodName)
	}
	_ = method // Could validate params against schema

	// Ensure environment is ready
	if err := e.manager.EnsureEnvironment(tool); err != nil {
		return nil, fmt.Errorf("failed to ensure environment: %w", err)
	}

	// Route based on transport
	transport := tool.Manifest.Runtime.Transport
	if transport == "msgpack" {
		if tool.Manifest.Runtime.Mode == "persistent" {
			return e.callPersistentMsgpack(tool, methodName, params)
		}
		return e.callOneshotMsgpack(tool, methodName, params)
	}

	// Default: REPL transport
	if tool.Manifest.Runtime.Mode == "persistent" {
		return e.callPersistent(tool, methodName, params)
	}
	return e.callOneshot(tool, methodName, params)
}

// callOneshot runs a tool for a single call using jb-service protocol
func (e *Executor) callOneshot(tool *Tool, methodName string, params map[string]interface{}) (interface{}, error) {
	entrypoint := filepath.Join(tool.Path, tool.Manifest.Runtime.Entrypoint)

	// Create REPL process - no module needed, we run main.py directly
	repl, err := tool.Env.NewREPLPythonProcess(nil, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create REPL: %w", err)
	}
	defer repl.Close()

	// Start the service by executing main.py
	if err := e.initializeService(repl, entrypoint, tool.Manifest.Runtime.StartupTimeout); err != nil {
		return nil, err
	}

	// Make the call using __jb_call__
	return e.doCall(repl, methodName, params)
}

// callPersistent calls a method on a running persistent tool (REPL transport)
func (e *Executor) callPersistent(tool *Tool, methodName string, params map[string]interface{}) (interface{}, error) {
	e.mu.RLock()
	repl, ok := e.repls[tool.Name]
	e.mu.RUnlock()

	if !ok || repl == nil {
		return nil, fmt.Errorf("tool %s is not running, start it first", tool.Name)
	}

	return e.doCall(repl, methodName, params)
}

// callOneshotMsgpack runs a tool for a single call using MessagePack transport
func (e *Executor) callOneshotMsgpack(tool *Tool, methodName string, params map[string]interface{}) (interface{}, error) {
	entrypoint := filepath.Join(tool.Path, tool.Manifest.Runtime.Entrypoint)

	// Create module from entrypoint
	mainModule, err := jumpboot.NewModuleFromPath("__main__", entrypoint)
	if err != nil {
		return nil, fmt.Errorf("failed to load entrypoint: %w", err)
	}

	// Create program
	program := &jumpboot.PythonProgram{
		Name:    tool.Name,
		Path:    tool.Path,
		Program: *mainModule,
	}

	// Create queue process
	queue, err := tool.Env.NewQueueProcess(program, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create queue process: %w", err)
	}
	defer queue.Close()

	// Call the method
	return e.doQueueCall(queue, methodName, params)
}

// callPersistentMsgpack calls a method on a running persistent tool (MessagePack transport)
func (e *Executor) callPersistentMsgpack(tool *Tool, methodName string, params map[string]interface{}) (interface{}, error) {
	e.mu.RLock()
	queue, ok := e.queues[tool.Name]
	e.mu.RUnlock()

	if !ok || queue == nil {
		return nil, fmt.Errorf("tool %s is not running, start it first", tool.Name)
	}

	return e.doQueueCall(queue, methodName, params)
}

// doQueueCall executes a method using MessagePack queue
func (e *Executor) doQueueCall(queue *jumpboot.QueueProcess, methodName string, params map[string]interface{}) (interface{}, error) {
	if params == nil {
		params = make(map[string]interface{})
	}

	// Call with 5 minute timeout
	result, err := queue.Call(methodName, 300, params)
	if err != nil {
		return nil, fmt.Errorf("queue call failed: %w", err)
	}

	return result, nil
}

// initializeService runs the tool's main.py and waits for __JB_READY__
func (e *Executor) initializeService(repl *jumpboot.REPLPythonProcess, entrypoint string, timeoutSec int) error {
	// Execute the entrypoint file with __name__ set to "__main__"
	// This is required for the `if __name__ == "__main__": run(Service)` pattern
	// The run() function registers __jb_call__ etc. in builtins
	initCode := fmt.Sprintf(`
__name__ = "__main__"
exec(open(%q).read())
`, entrypoint)
	
	_, err := repl.Execute(initCode, true)
	if err != nil {
		return fmt.Errorf("failed to run entrypoint: %w", err)
	}

	// Import __jb_call__ from builtins into the REPL's namespace
	// This makes it directly callable without the builtins prefix
	importCode := `
import builtins
if hasattr(builtins, '__jb_call__'):
    __jb_call__ = builtins.__jb_call__
    __jb_schema__ = builtins.__jb_schema__
    __jb_methods__ = builtins.__jb_methods__
    __jb_shutdown__ = builtins.__jb_shutdown__
`
	_, err = repl.Execute(importCode, true)
	if err != nil {
		return fmt.Errorf("failed to import jb functions: %w", err)
	}

	// Check that __jb_call__ is available
	checkCode := `"ready" if callable(globals().get("__jb_call__")) else "not ready"`
	result, err := repl.Execute(checkCode, true)
	if err != nil {
		return fmt.Errorf("failed to check service status: %w", err)
	}

	if !strings.Contains(result, "ready") {
		return fmt.Errorf("service did not initialize properly: __jb_call__ not found")
	}

	return nil
}

// doCall executes a method using the __jb_call__ protocol
func (e *Executor) doCall(repl *jumpboot.REPLPythonProcess, methodName string, params map[string]interface{}) (interface{}, error) {
	if params == nil {
		params = make(map[string]interface{})
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal params: %w", err)
	}

	// Call __jb_call__(method, params)
	callExpr := fmt.Sprintf(`__jb_call__(%q, %s)`, methodName, string(paramsJSON))
	
	result, err := repl.Execute(callExpr, true)
	if err != nil {
		return nil, fmt.Errorf("call failed: %w", err)
	}

	// Parse the response
	return e.parseResponse(result)
}

// parseResponse parses a jb-service response
func (e *Executor) parseResponse(result string) (interface{}, error) {
	// Clean up the result string - REPL may return quoted strings
	resultStr := strings.TrimSpace(result)
	
	// Remove outer quotes if present
	if len(resultStr) >= 2 {
		if (resultStr[0] == '\'' && resultStr[len(resultStr)-1] == '\'') ||
			(resultStr[0] == '"' && resultStr[len(resultStr)-1] == '"') {
			resultStr = resultStr[1 : len(resultStr)-1]
		}
	}

	// Handle Python string escaping
	resultStr = strings.ReplaceAll(resultStr, `\'`, `'`)
	resultStr = strings.ReplaceAll(resultStr, `\"`, `"`)

	// Parse as CallResponse
	var resp CallResponse
	if err := json.Unmarshal([]byte(resultStr), &resp); err != nil {
		// If not valid JSON, return raw result
		return result, nil
	}

	// Check for error
	if !resp.OK {
		if resp.Error != nil {
			errMsg := fmt.Sprintf("%s: %s", resp.Error.Type, resp.Error.Message)
			if resp.Error.Traceback != "" {
				errMsg += "\n" + resp.Error.Traceback
			}
			return nil, fmt.Errorf(errMsg)
		}
		return nil, fmt.Errorf("call failed with unknown error")
	}

	return resp.Result, nil
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

	// Check if already running
	if _, ok := e.repls[toolName]; ok {
		return fmt.Errorf("tool %s is already running", toolName)
	}
	if _, ok := e.queues[toolName]; ok {
		return fmt.Errorf("tool %s is already running", toolName)
	}

	// Ensure environment
	if err := e.manager.EnsureEnvironment(tool); err != nil {
		return err
	}

	// Start based on transport
	transport := tool.Manifest.Runtime.Transport
	if transport == "msgpack" {
		return e.startMsgpack(tool)
	}
	return e.startRepl(tool)
}

// startRepl starts a tool with REPL transport
func (e *Executor) startRepl(tool *Tool) error {
	repl, err := tool.Env.NewREPLPythonProcess(nil, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to create REPL: %w", err)
	}

	// Initialize the service
	entrypoint := filepath.Join(tool.Path, tool.Manifest.Runtime.Entrypoint)
	if err := e.initializeService(repl, entrypoint, tool.Manifest.Runtime.StartupTimeout); err != nil {
		repl.Close()
		return fmt.Errorf("failed to initialize service: %w", err)
	}

	e.repls[tool.Name] = repl
	tool.Status = "running"
	tool.HealthStatus = "unknown"
	tool.HealthFailures = 0

	// Start health check if configured
	if tool.Manifest.Health != nil {
		ctx, cancel := context.WithCancel(context.Background())
		e.healthCancels[tool.Name] = cancel
		go e.runHealthCheck(ctx, tool)
	}

	log.Printf("Started %s (REPL)", tool.Name)
	return nil
}

// startMsgpack starts a tool with MessagePack transport
func (e *Executor) startMsgpack(tool *Tool) error {
	entrypoint := filepath.Join(tool.Path, tool.Manifest.Runtime.Entrypoint)

	// Create module from entrypoint
	mainModule, err := jumpboot.NewModuleFromPath("__main__", entrypoint)
	if err != nil {
		return fmt.Errorf("failed to load entrypoint: %w", err)
	}

	// Create program
	program := &jumpboot.PythonProgram{
		Name:    tool.Name,
		Path:    tool.Path,
		Program: *mainModule,
	}

	// Create queue process
	queue, err := tool.Env.NewQueueProcess(program, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to create queue process: %w", err)
	}

	e.queues[tool.Name] = queue
	tool.Status = "running"
	tool.HealthStatus = "unknown"
	tool.HealthFailures = 0

	// Start health check if configured
	if tool.Manifest.Health != nil {
		ctx, cancel := context.WithCancel(context.Background())
		e.healthCancels[tool.Name] = cancel
		go e.runHealthCheckMsgpack(ctx, tool)
	}

	log.Printf("Started %s (MessagePack)", tool.Name)
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

	// Cancel health check if running
	if cancel, ok := e.healthCancels[toolName]; ok {
		cancel()
		delete(e.healthCancels, toolName)
	}

	// Stop REPL if running
	if repl, ok := e.repls[toolName]; ok {
		repl.Close()
		delete(e.repls, toolName)
		tool.Status = "stopped"
		tool.HealthStatus = ""
		tool.HealthFailures = 0
		log.Printf("Stopped %s (REPL)", toolName)
		return nil
	}

	// Stop queue if running
	if queue, ok := e.queues[toolName]; ok {
		queue.Close()
		delete(e.queues, toolName)
		tool.Status = "stopped"
		tool.HealthStatus = ""
		tool.HealthFailures = 0
		log.Printf("Stopped %s (MessagePack)", toolName)
		return nil
	}

	return fmt.Errorf("tool %s is not running", toolName)
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

	// Close all queues
	for name, queue := range e.queues {
		queue.Close()
		if tool, ok := e.manager.Get(name); ok {
			tool.Status = "stopped"
			tool.HealthStatus = ""
		}
	}
	e.queues = make(map[string]*jumpboot.QueueProcess)
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

// doHealthCheck performs a single health check call using __jb_call__ (REPL)
func (e *Executor) doHealthCheck(tool *Tool, method string) error {
	e.mu.RLock()
	repl, ok := e.repls[tool.Name]
	e.mu.RUnlock()

	if !ok || repl == nil {
		return fmt.Errorf("tool not running")
	}

	// Call the health method via __jb_call__
	result, err := e.doCall(repl, method, nil)
	if err != nil {
		return fmt.Errorf("health call failed: %w", err)
	}

	return e.checkHealthResult(result)
}

// runHealthCheckMsgpack runs periodic health checks for a tool (MessagePack transport)
func (e *Executor) runHealthCheckMsgpack(ctx context.Context, tool *Tool) {
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
			err := e.doHealthCheckMsgpack(tool, method)
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

// doHealthCheckMsgpack performs a single health check call (MessagePack)
func (e *Executor) doHealthCheckMsgpack(tool *Tool, method string) error {
	e.mu.RLock()
	queue, ok := e.queues[tool.Name]
	e.mu.RUnlock()

	if !ok || queue == nil {
		return fmt.Errorf("tool not running")
	}

	// Call the health method
	result, err := e.doQueueCall(queue, method, nil)
	if err != nil {
		return fmt.Errorf("health call failed: %w", err)
	}

	return e.checkHealthResult(result)
}

// checkHealthResult validates the health check response
func (e *Executor) checkHealthResult(result interface{}) error {
	// Check for "ok" or "status: ok" in result
	if resultMap, ok := result.(map[string]interface{}); ok {
		if status, ok := resultMap["status"].(string); ok && status == "ok" {
			return nil
		}
	}

	// Also accept simple "ok" string
	if resultStr, ok := result.(string); ok && resultStr == "ok" {
		return nil
	}

	return fmt.Errorf("unhealthy status: %v", result)
}

// GetSchema returns the schema for a tool (via __jb_schema__)
func (e *Executor) GetSchema(toolName string) (map[string]interface{}, error) {
	tool, ok := e.manager.Get(toolName)
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", toolName)
	}

	// For persistent tools, use running REPL
	if tool.Manifest.Runtime.Mode == "persistent" {
		e.mu.RLock()
		repl, ok := e.repls[toolName]
		e.mu.RUnlock()

		if ok && repl != nil {
			return e.getSchemaFromRepl(repl)
		}
	}

	// For oneshot or stopped persistent, create temporary REPL
	if err := e.manager.EnsureEnvironment(tool); err != nil {
		return nil, err
	}

	repl, err := tool.Env.NewREPLPythonProcess(nil, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	defer repl.Close()

	entrypoint := filepath.Join(tool.Path, tool.Manifest.Runtime.Entrypoint)
	if err := e.initializeService(repl, entrypoint, tool.Manifest.Runtime.StartupTimeout); err != nil {
		return nil, err
	}

	return e.getSchemaFromRepl(repl)
}

// getSchemaFromRepl gets schema using __jb_schema__
func (e *Executor) getSchemaFromRepl(repl *jumpboot.REPLPythonProcess) (map[string]interface{}, error) {
	result, err := repl.Execute("__jb_schema__()", true)
	if err != nil {
		return nil, fmt.Errorf("failed to get schema: %w", err)
	}

	resultStr := strings.TrimSpace(result)
	if len(resultStr) >= 2 {
		if (resultStr[0] == '\'' && resultStr[len(resultStr)-1] == '\'') ||
			(resultStr[0] == '"' && resultStr[len(resultStr)-1] == '"') {
			resultStr = resultStr[1 : len(resultStr)-1]
		}
	}
	resultStr = strings.ReplaceAll(resultStr, `\'`, `'`)

	var schema map[string]interface{}
	if err := json.Unmarshal([]byte(resultStr), &schema); err != nil {
		return nil, fmt.Errorf("failed to parse schema: %w", err)
	}

	return schema, nil
}
