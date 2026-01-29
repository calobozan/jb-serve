package config

// Manifest represents a jumpboot.yaml tool manifest
type Manifest struct {
	Name         string       `yaml:"name"`
	Version      string       `yaml:"version"`
	Description  string       `yaml:"description"`
	Capabilities []string     `yaml:"capabilities,omitempty"`
	Runtime      Runtime      `yaml:"runtime"`
	Resources    Resources    `yaml:"resources,omitempty"`
	RPC          RPC          `yaml:"rpc"`
	Health       *Health      `yaml:"health,omitempty"`
}

// Runtime defines the Python environment requirements
type Runtime struct {
	Python         string   `yaml:"python"`                    // Python version (e.g., "3.11")
	Requirements   string   `yaml:"requirements,omitempty"`    // requirements.txt path
	Packages       []string `yaml:"packages,omitempty"`        // pip packages to install
	CondaPackages  []string `yaml:"conda_packages,omitempty"`  // conda packages
	Mode           string   `yaml:"mode"`                      // "persistent" or "oneshot"
	Entrypoint     string   `yaml:"entrypoint,omitempty"`      // main script, defaults to main.py
	StartupTimeout int      `yaml:"startup_timeout,omitempty"` // seconds
}

// Resources defines resource hints for scheduling
type Resources struct {
	GPU    bool `yaml:"gpu,omitempty"`
	VRAMGB int  `yaml:"vram_gb,omitempty"`
	RAMGB  int  `yaml:"ram_gb,omitempty"`
}

// RPC defines the tool's RPC interface
type RPC struct {
	Transport string            `yaml:"transport,omitempty"` // "http" (default) or "jsonqueue"
	Port      interface{}       `yaml:"port,omitempty"`      // "auto" or fixed number
	Methods   map[string]Method `yaml:"methods"`
}

// Method defines a single RPC method
type Method struct {
	Description string  `yaml:"description"`
	Input       *Schema `yaml:"input,omitempty"`
	Output      *Schema `yaml:"output,omitempty"`
}

// Schema is a simplified JSON Schema
type Schema struct {
	Type       string             `yaml:"type,omitempty"`
	Properties map[string]*Schema `yaml:"properties,omitempty"`
	Required   []string           `yaml:"required,omitempty"`
	Items      *Schema            `yaml:"items,omitempty"`
	Default    interface{}        `yaml:"default,omitempty"`
	Desc       string             `yaml:"description,omitempty"`
}

// Health defines health check configuration
type Health struct {
	Endpoint string `yaml:"endpoint,omitempty"` // default: /health
	Interval int    `yaml:"interval,omitempty"` // seconds
}

// ApplyDefaults fills in sensible defaults
func (m *Manifest) ApplyDefaults() {
	if m.Runtime.Mode == "" {
		m.Runtime.Mode = "oneshot"
	}
	if m.Runtime.Entrypoint == "" {
		m.Runtime.Entrypoint = "main.py"
	}
	if m.Runtime.StartupTimeout == 0 {
		m.Runtime.StartupTimeout = 60
	}
	if m.RPC.Transport == "" {
		m.RPC.Transport = "jsonqueue"
	}
	if m.Health != nil && m.Health.Endpoint == "" {
		m.Health.Endpoint = "/health"
	}
}
