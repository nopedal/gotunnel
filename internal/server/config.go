package server

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds the server configuration.
type Config struct {
	Domain      string `yaml:"domain"`
	Token       string `yaml:"token"`
	ControlPort int    `yaml:"control_port"`
	HTTPPort    int    `yaml:"http_port"`
	HTTPSPort   int    `yaml:"https_port"`
	MaxClients  int    `yaml:"max_clients"`
	CertDir     string `yaml:"cert_dir"`
	TLSEnabled  bool   `yaml:"tls_enabled"`
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Domain:      "tunnel.example.com",
		Token:       "",
		ControlPort: 7000,
		HTTPPort:    80,
		HTTPSPort:   443,
		MaxClients:  50,
		CertDir:     "/var/lib/tunnel/certs",
		TLSEnabled:  true,
	}
}

// LoadConfig reads the YAML config file and merges with defaults.
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Token == "" {
		return nil, fmt.Errorf("config: token is required")
	}
	if cfg.Domain == "" {
		return nil, fmt.Errorf("config: domain is required")
	}

	return cfg, nil
}
