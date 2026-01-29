package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the global jb-serve configuration
type Config struct {
	ToolsDir  string `yaml:"tools_dir"`  // Where tools are installed
	EnvsDir   string `yaml:"envs_dir"`   // Where jumpboot environments live
	RunDir    string `yaml:"run_dir"`    // Runtime state (pids, sockets)
	APIPort   int    `yaml:"api_port"`   // Default API server port
	AuthToken string `yaml:"auth_token"` // Optional auth token
}

// DefaultConfig returns config with default paths
func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".jb-serve")

	return &Config{
		ToolsDir: filepath.Join(base, "tools"),
		EnvsDir:  filepath.Join(base, "envs"),
		RunDir:   filepath.Join(base, "run"),
		APIPort:  9800,
	}
}

// BaseDir returns the jb-serve base directory
func (c *Config) BaseDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".jb-serve")
}

// ConfigPath returns the path to the config file
func (c *Config) ConfigPath() string {
	return filepath.Join(c.BaseDir(), "config.yaml")
}

// Load reads config from disk, or returns defaults
func Load() (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(cfg.ConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // Use defaults
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Save writes config to disk
func (c *Config) Save() error {
	if err := os.MkdirAll(c.BaseDir(), 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(c.ConfigPath(), data, 0644)
}

// EnsureDirs creates all necessary directories
func (c *Config) EnsureDirs() error {
	dirs := []string{c.ToolsDir, c.EnvsDir, c.RunDir}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return nil
}
