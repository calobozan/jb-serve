package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/calobozan/jb-serve/internal/config"
	"github.com/richinsley/jumpboot"
	"gopkg.in/yaml.v3"
)

// Tool represents an installed tool with its jumpboot environment
type Tool struct {
	Name           string                      `json:"name"`
	Path           string                      `json:"path"`             // Filesystem path to tool repo
	Manifest       *config.Manifest            `json:"manifest"`
	Env            *jumpboot.PythonEnvironment `json:"-"`                // Jumpboot environment
	Status         string                      `json:"status"`           // "stopped", "running"
	HealthStatus   string                      `json:"health_status"`    // "healthy", "unhealthy", "unknown"
	HealthFailures int                         `json:"health_failures"`  // Consecutive health check failures
	PID            int                         `json:"pid,omitempty"`
	Port           int                         `json:"port,omitempty"`
}

// Manager handles tool lifecycle using jumpboot
type Manager struct {
	cfg   *config.Config
	tools map[string]*Tool
}

// NewManager creates a new tool manager
func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		cfg:   cfg,
		tools: make(map[string]*Tool),
	}
}

// LoadAll scans the tools directory and loads all manifests
func (m *Manager) LoadAll() error {
	entries, err := os.ReadDir(m.cfg.ToolsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		toolPath := filepath.Join(m.cfg.ToolsDir, entry.Name())

		// Follow symlinks
		info, err := os.Stat(toolPath)
		if err != nil || !info.IsDir() {
			continue
		}

		manifest, err := m.loadManifest(toolPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load %s: %v\n", entry.Name(), err)
			continue
		}

		m.tools[manifest.Name] = &Tool{
			Name:     manifest.Name,
			Path:     toolPath,
			Manifest: manifest,
			Status:   "stopped",
		}
	}

	return nil
}

// loadManifest reads and parses a jumpboot.yaml
func (m *Manager) loadManifest(toolPath string) (*config.Manifest, error) {
	manifestPath := filepath.Join(toolPath, "jumpboot.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}

	var manifest config.Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}

	manifest.ApplyDefaults()
	return &manifest, nil
}

// Install installs a tool from git URL or local path
func (m *Manager) Install(source string) (*Tool, error) {
	var toolPath string
	var err error

	// Determine if source is local or git
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "~") {
		toolPath, err = m.installLocal(source)
	} else {
		toolPath, err = m.installGit(source)
	}
	if err != nil {
		return nil, err
	}

	// Load manifest
	manifest, err := m.loadManifest(toolPath)
	if err != nil {
		os.RemoveAll(toolPath)
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	// Check if already installed
	if existing, ok := m.tools[manifest.Name]; ok {
		os.RemoveAll(toolPath)
		return nil, fmt.Errorf("tool %s already installed at %s", manifest.Name, existing.Path)
	}

	// Create jumpboot environment
	fmt.Printf("Creating Python %s environment for %s...\n", manifest.Runtime.Python, manifest.Name)
	env, err := m.createEnvironment(manifest)
	if err != nil {
		os.RemoveAll(toolPath)
		return nil, fmt.Errorf("failed to create environment: %w", err)
	}

	// Install packages if environment is new
	if env.IsNew {
		if err := m.installPackages(env, manifest, toolPath); err != nil {
			return nil, fmt.Errorf("failed to install packages: %w", err)
		}
	}

	tool := &Tool{
		Name:     manifest.Name,
		Path:     toolPath,
		Manifest: manifest,
		Env:      env,
		Status:   "stopped",
	}
	m.tools[manifest.Name] = tool

	fmt.Printf("Installed %s v%s\n", manifest.Name, manifest.Version)
	return tool, nil
}

// createEnvironment creates a jumpboot environment for a tool
func (m *Manager) createEnvironment(manifest *config.Manifest) (*jumpboot.PythonEnvironment, error) {
	envName := fmt.Sprintf("tool-%s", manifest.Name)
	
	// Use jumpboot to create isolated Python environment
	env, err := jumpboot.CreateEnvironmentMamba(
		envName,
		m.cfg.EnvsDir,
		manifest.Runtime.Python,
		"conda-forge",
		nil, // progress callback
	)
	if err != nil {
		return nil, err
	}

	return env, nil
}

// installPackages installs pip/conda packages into the environment
func (m *Manager) installPackages(env *jumpboot.PythonEnvironment, manifest *config.Manifest, toolPath string) error {
	// Install conda packages first (one at a time via micromamba)
	if len(manifest.Runtime.CondaPackages) > 0 {
		fmt.Printf("Installing conda packages: %v\n", manifest.Runtime.CondaPackages)
		for _, pkg := range manifest.Runtime.CondaPackages {
			if err := env.MicromambaInstallPackage(pkg, "conda-forge"); err != nil {
				return err
			}
		}
	}

	// Install pip packages
	if len(manifest.Runtime.Packages) > 0 {
		fmt.Printf("Installing pip packages: %v\n", manifest.Runtime.Packages)
		if err := env.PipInstallPackages(manifest.Runtime.Packages, "", "", false, nil); err != nil {
			return err
		}
	}

	// Install from requirements.txt
	if manifest.Runtime.Requirements != "" {
		reqPath := filepath.Join(toolPath, manifest.Runtime.Requirements)
		if _, err := os.Stat(reqPath); err == nil {
			fmt.Printf("Installing from %s\n", manifest.Runtime.Requirements)
			if err := env.PipInstallRequirements(reqPath, nil); err != nil {
				return err
			}
		}
	}

	return nil
}

// installLocal symlinks a local tool
func (m *Manager) installLocal(source string) (string, error) {
	// Expand ~
	if strings.HasPrefix(source, "~") {
		home, _ := os.UserHomeDir()
		source = filepath.Join(home, source[1:])
	}

	absSource, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}

	// Check manifest exists
	if _, err := os.Stat(filepath.Join(absSource, "jumpboot.yaml")); err != nil {
		return "", fmt.Errorf("no jumpboot.yaml found at %s", source)
	}

	// Load manifest to get name
	manifest, err := m.loadManifest(absSource)
	if err != nil {
		return "", err
	}

	toolPath := filepath.Join(m.cfg.ToolsDir, manifest.Name)
	os.RemoveAll(toolPath)

	if err := os.Symlink(absSource, toolPath); err != nil {
		return "", fmt.Errorf("failed to create symlink: %w", err)
	}

	return toolPath, nil
}

// installGit clones a tool from git
func (m *Manager) installGit(source string) (string, error) {
	gitURL := source
	if !strings.HasPrefix(gitURL, "https://") && !strings.HasPrefix(gitURL, "git@") {
		gitURL = "https://" + source
		if !strings.Contains(gitURL, ".git") {
			gitURL += ".git"
		}
	}

	tempDir, err := os.MkdirTemp("", "jb-serve-install-")
	if err != nil {
		return "", err
	}

	fmt.Printf("Cloning %s...\n", gitURL)
	cmd := exec.Command("git", "clone", "--depth", "1", gitURL, tempDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(tempDir)
		return "", fmt.Errorf("git clone failed: %w", err)
	}

	manifest, err := m.loadManifest(tempDir)
	if err != nil {
		os.RemoveAll(tempDir)
		return "", err
	}

	toolPath := filepath.Join(m.cfg.ToolsDir, manifest.Name)
	os.RemoveAll(toolPath)

	if err := os.Rename(tempDir, toolPath); err != nil {
		os.RemoveAll(tempDir)
		return "", fmt.Errorf("failed to move tool: %w", err)
	}

	return toolPath, nil
}

// Get returns a tool by name
func (m *Manager) Get(name string) (*Tool, bool) {
	t, ok := m.tools[name]
	return t, ok
}

// List returns all installed tools
func (m *Manager) List() []*Tool {
	tools := make([]*Tool, 0, len(m.tools))
	for _, t := range m.tools {
		tools = append(tools, t)
	}
	return tools
}

// ListJSON returns tools as JSON
func (m *Manager) ListJSON() ([]byte, error) {
	return json.MarshalIndent(m.List(), "", "  ")
}

// ToolInfo returns agent-friendly info about a tool
type ToolInfo struct {
	Name         string                   `json:"name"`
	Version      string                   `json:"version"`
	Description  string                   `json:"description"`
	Capabilities []string                 `json:"capabilities"`
	Mode         string                   `json:"mode"`
	Status       string                   `json:"status"`
	HealthStatus string                   `json:"health_status,omitempty"`
	Methods      map[string]config.Method `json:"methods"`
}

// Info returns detailed info about a tool
func (m *Manager) Info(name string) (*ToolInfo, error) {
	tool, ok := m.tools[name]
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}

	return &ToolInfo{
		Name:         tool.Manifest.Name,
		Version:      tool.Manifest.Version,
		Description:  tool.Manifest.Description,
		Capabilities: tool.Manifest.Capabilities,
		Mode:         tool.Manifest.Runtime.Mode,
		Status:       tool.Status,
		HealthStatus: tool.HealthStatus,
		Methods:      tool.Manifest.RPC.Methods,
	}, nil
}

// EnsureEnvironment loads or creates the jumpboot environment for a tool
func (m *Manager) EnsureEnvironment(tool *Tool) error {
	if tool.Env != nil {
		return nil
	}

	env, err := m.createEnvironment(tool.Manifest)
	if err != nil {
		return err
	}

	if env.IsNew {
		if err := m.installPackages(env, tool.Manifest, tool.Path); err != nil {
			return err
		}
	}

	tool.Env = env
	return nil
}
