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
	// Models maps the model ID of a request to the provider that serves it.
	// Models that are not listed go to Anthropic.
	Models map[string]Route `toml:"models"`
	// Menu lists the models to show in the model menu of Claude Desktop,
	// with the names to show them under. It is the alternative to giving
	// models the IDs of Claude models, and needs a certificate in the
	// user's trust store; see the README.
	Menu map[string]string `toml:"menu"`
}

// Provider is a third-party provider of the Anthropic Messages API.
type Provider struct {
	BaseURL string `toml:"base_url"`
	// APIKey is sent to the provider as a bearer token. A local server
	// may need none.
	APIKey string `toml:"api_key"`
}

// Route says which provider serves a model and under what name. It is
// written as "provider", or as "provider/model" when the provider knows the
// model under a name of its own.
type Route struct {
	Provider string
	Model    string
}

// UnmarshalText parses the "provider" and "provider/model" forms.
func (r *Route) UnmarshalText(text []byte) error {
	r.Provider, r.Model, _ = strings.Cut(string(text), "/")
	return nil
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
	for model, route := range c.Models {
		// The ID ends up inside thinking signatures, delimited by spaces.
		if model == "" || strings.ContainsAny(model, " \t\n") {
			return fmt.Errorf("model %q: the ID must not be empty or contain whitespace", model)
		}
		if _, ok := c.Providers[route.Provider]; !ok {
			return fmt.Errorf("model %q: unknown provider %q", model, route.Provider)
		}
		if route.Model == "" {
			route.Model = model
			c.Models[model] = route
		}
	}
	for model := range c.Menu {
		if _, ok := c.Models[model]; !ok {
			return fmt.Errorf("menu: model %q is not listed in [models]", model)
		}
	}
	// A model reaches the Desktop menu in one of two ways: it takes the
	// place of a Claude model, or it gets an entry of its own. Mixing the
	// two would show the same models twice, under different names.
	if len(c.Menu) > 0 {
		for model := range c.Models {
			if strings.HasPrefix(model, claudePrefix) {
				return fmt.Errorf("model %q: with [menu] set, models cannot take the IDs of Claude models", model)
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
