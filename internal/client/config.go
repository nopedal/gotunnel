package client

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds the client configuration.
type Config struct {
	Server    string `yaml:"server"`
	Token     string `yaml:"token"`
	Port      int    `yaml:"port"`
	Subdomain string `yaml:"subdomain"`
	Mode      string `yaml:"mode"`     // "http" or "tcp"
	Insecure  bool   `yaml:"insecure"` // plain TCP, for servers with tls_enabled: false
}

// DefaultConfig returns client defaults.
func DefaultConfig() *Config {
	return &Config{
		Mode: "http",
	}
}

// LoadClientConfig reads ~/.tunnel/config.yaml if it exists.
func LoadClientConfig() *Config {
	cfg := DefaultConfig()

	home, err := os.UserHomeDir()
	if err != nil {
		return cfg
	}

	path := filepath.Join(home, ".tunnel", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}

	yaml.Unmarshal(data, cfg)
	return cfg
}
