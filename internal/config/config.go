// Package config loads the gateway configuration from a TOML file.
package config

import (
	_ "embed"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the gateway configuration.
type Config struct {
	// Listen is the loopback address the gateway accepts requests on.
	Listen string `toml:"listen"`
	// Providers are third-party Anthropic-compatible providers by name.
	Providers map[string]Provider `toml:"providers"`
	// Models are the models served by the providers. A model that is not
	// listed goes to Anthropic.
	Models []Model `toml:"model"`
}

// Provider is a third-party provider of the Anthropic Messages API.
type Provider struct {
	BaseURL string `toml:"base_url"`
	// APIKey is sent to the provider as a bearer token. A local server
	// may need none.
	APIKey string `toml:"api_key"`
}

// Model is a model of a third-party provider.
type Model struct {
	// ID is what the user picks in Claude Code and what requests carry.
	ID string `toml:"id"`
	// Provider names the entry of Providers that serves the model.
	Provider string `toml:"provider"`
	// Upstream is the model's name at the provider. It defaults to ID.
	Upstream string `toml:"upstream"`
	// Menu is the name of the model's own entry in the model menu of
	// Claude Desktop. It is optional, and needs a certificate in the
	// user's trust store; see the README.
	Menu string `toml:"menu"`
}

// Menu returns the names of the models that get an entry of their own in
// the menu of Claude Desktop, by model ID.
func (c *Config) Menu() map[string]string {
	names := map[string]string{}
	for _, m := range c.Models {
		if m.Menu != "" {
			names[m.ID] = m.Menu
		}
	}
	return names
}

// Example is a configuration file with every setting explained. It is what a
// new user starts from.
//
//go:embed example.toml
var Example []byte

// Create writes Example to path for the user to edit. The file is readable
// by the user only: it will hold provider keys.
func Create(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, Example, 0o600)
}

// DefaultPath returns the default location of the configuration file.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".autoclaude", "config.toml")
}

// Load reads and validates the configuration file.
func Load(path string) (*Config, error) {
	cfg := &Config{Listen: "127.0.0.1:8484"}
	meta, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return nil, err
	}
	if unknown := meta.Undecoded(); len(unknown) > 0 {
		return nil, fmt.Errorf("%s: unknown key %q", path, unknown[0])
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	// The gateway forwards subscription credentials and decrypts traffic:
	// it must not be reachable from other machines.
	if host, _, err := net.SplitHostPort(c.Listen); err != nil || !isLoopback(host) {
		return fmt.Errorf("listen must be a loopback address, got %q", c.Listen)
	}
	for name, p := range c.Providers {
		u, err := url.Parse(p.BaseURL)
		if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" || u.RawQuery != "" {
			return fmt.Errorf("provider %q: base_url must be an http(s) URL without a query, got %q", name, p.BaseURL)
		}
	}
	seen := map[string]bool{}
	for i := range c.Models {
		m := &c.Models[i]
		// The ID ends up inside thinking signatures, delimited by spaces.
		if m.ID == "" || strings.ContainsAny(m.ID, " \t\n") {
			return fmt.Errorf("model %q: the id must not be empty or contain whitespace", m.ID)
		}
		if seen[m.ID] {
			return fmt.Errorf("model %q is listed twice", m.ID)
		}
		seen[m.ID] = true
		if _, ok := c.Providers[m.Provider]; !ok {
			return fmt.Errorf("model %q: unknown provider %q", m.ID, m.Provider)
		}
		if m.Upstream == "" {
			m.Upstream = m.ID
		}
	}
	// A model reaches the Desktop menu in one of two ways: it takes the
	// place of a Claude model, or it gets an entry of its own. Mixing the
	// two would show the same models twice, under different names.
	if len(c.Menu()) > 0 {
		for _, m := range c.Models {
			if strings.HasPrefix(m.ID, claudePrefix) {
				return fmt.Errorf("model %q: models cannot take the IDs of Claude models while others have a menu name", m.ID)
			}
		}
	}
	return nil
}

// claudePrefix starts the ID of every Claude model.
const claudePrefix = "claude-"

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}
