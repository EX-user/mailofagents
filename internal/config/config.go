// Package config loads the agentmail-server configuration.
//
// The TOML file holds runtime settings: where to listen and where the bbolt
// database lives. For unattended init (--yes-init-from-config), it can also
// carry first-time init fields (domain, admin password). For the normal and
// browser-wizard paths, those init fields are ignored — domain and admin
// credentials are set via the wizard and persisted in bbolt.
package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration object for agentmail-server.
type Config struct {
	Server  ServerConfig  `toml:"server"`
	Admin   AdminConfig   `toml:"admin"`
	Storage StorageConfig `toml:"storage"`
	Push    PushConfig    `toml:"push"`
}

// PushConfig carries the Web Push VAPID key pair (v0.6.30 app notifications).
// The private key lives ONLY in the deployment config — never in bbolt, never
// in the repo (generate with `pushkeygen`). An empty public key means push is
// disabled and the public endpoint reports as such.
type PushConfig struct {
	VAPIDPublicKey  string `toml:"vapid_public_key"`
	VAPIDPrivateKey string `toml:"vapid_private_key"`
	Subject         string `toml:"subject"` // mailto: or https: contact for the push service
}

// ServerConfig describes the HTTP listener and (for init only) the mail domain.
type ServerConfig struct {
	Listen string `toml:"listen"`
	// Domain is only read during --yes-init-from-config. Normal/wizard mode
	// reads the domain from bbolt (set by the wizard).
	Domain string `toml:"domain"`
}

// AdminConfig holds the admin password for --yes-init-from-config only.
// In normal/wizard mode this is ignored — admin credentials live in bbolt.
type AdminConfig struct {
	Password string `toml:"password"`
}

// StorageConfig describes where the bbolt database lives.
type StorageConfig struct {
	DBPath string `toml:"db_path"`
}

// Load reads and validates the config file at path. If path is "" the defaults
// are used (no file required).
func Load(path string) (*Config, error) {
	cfg := defaults()

	if path != "" {
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, fmt.Errorf("decode config %q: %w", path, err)
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Listen: "127.0.0.1:8090",
		},
		Storage: StorageConfig{
			DBPath: "agentmail.db",
		},
	}
}

func (c *Config) validate() error {
	if c.Server.Listen == "" {
		return fmt.Errorf("server.listen must be set")
	}
	if c.Storage.DBPath == "" {
		return fmt.Errorf("storage.db_path must be set")
	}
	return nil
}

// HasInitConfig reports whether the config has the fields needed for
// --yes-init-from-config (domain + admin password).
func (c *Config) HasInitConfig() bool {
	return c.Server.Domain != "" && c.Admin.Password != ""
}

// DefaultConfigPath returns the config path from AGENTMAIL_CONFIG, then falls
// back to agentmail.toml in the executable's directory, then "".
func DefaultConfigPath() string {
	if p := os.Getenv("AGENTMAIL_CONFIG"); p != "" {
		return p
	}
	// Check for agentmail.toml next to the executable.
	if exe, err := os.Executable(); err == nil {
		p := exeDir(exe) + string(os.PathSeparator) + "agentmail.toml"
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func exeDir(exe string) string {
	for i := len(exe) - 1; i >= 0; i-- {
		if exe[i] == os.PathSeparator || exe[i] == '/' {
			return exe[:i]
		}
	}
	return "."
}
