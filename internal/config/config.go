// Package config loads the gateway configuration from a TOML file.
package config

import (
	_ "embed"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the gateway configuration.
type Config struct {
	// Listen is the loopback address the gateway accepts requests on.
	Listen string `toml:"listen"`
	// Desktop chooses how the models reach the model menu of Claude
	// Desktop: Slots or Entries.
	Desktop string `toml:"desktop"`
	// ContextWindow is the context window, in tokens, that Claude Code is
	// told to assume for the models. Claude Code has one such value for
	// all the models it does not know. Zero leaves its own assumption.
	ContextWindow int `toml:"context_window"`
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
	// Name is what the model is called in menus: in the model picker of
	// the CLI and, with Entries, in the menu of Claude Desktop.
	// It defaults to ID.
	Name string `toml:"name"`
	// Context is the model's context window, in tokens, for Claude Code to
	// show and to compact the conversation by. Zero is unknown.
	Context int `toml:"context"`
	// Slot is the ID of the Claude model whose entry in the Desktop menu
	// the model takes. It is used with Slots.
	Slot string `toml:"slot"`
}

// The ways for models to reach the model menu of Claude Desktop.
const (
	// Slots puts a model in place of the Claude model named by its Slot.
	Slots = "slots"
	// Entries gives the models menu entries of their own. It needs a
	// certificate for claude.ai in the user's trust store.
	Entries = "entries"
)

// Default returns the configuration of a gateway that knows no models and
// passes everything to Anthropic.
func Default() *Config {
	return &Config{Listen: "127.0.0.1:8484", Desktop: Slots}
}

// OwnEntries reports whether the models get entries of their own in the
// menu of Claude Desktop.
func (c *Config) OwnEntries() bool {
	return c.Desktop == Entries
}

// Menu returns the models that have entries of their own in the Desktop
// menu.
func (c *Config) Menu() []Model {
	if !c.OwnEntries() {
		return nil
	}
	return c.Models
}

// Routes returns the model for every model ID the gateway answers to: the
// models' own IDs and, with Slots, the Claude IDs they take.
func (c *Config) Routes() map[string]Model {
	routes := map[string]Model{}
	for _, m := range c.Models {
		routes[m.ID] = m
	}
	for _, m := range c.Models {
		if !c.OwnEntries() && m.Slot != "" {
			routes[m.Slot] = m
		}
	}
	return routes
}

// The context windows Claude Code accepts for a model, in tokens.
const (
	MinContext = 100_000
	MaxContext = 10_000_000
)

// Example is a short configuration file with examples to turn on. It is
// what a new user starts from.
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

var desktopSetting = regexp.MustCompile(`(?m)^desktop[ \t]*=[ \t]*"[^"\n]*"`)

// SetDesktop writes the way into the Desktop menu to the configuration file
// at path and leaves the rest of the file, comments included, as it is.
func SetDesktop(path, desktop string) error {
	file, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	setting := []byte(fmt.Sprintf("desktop = %q", desktop))
	if desktopSetting.Match(file) {
		file = desktopSetting.ReplaceAllLiteral(file, setting)
	} else {
		// A setting of the file itself has to come before its first table.
		file = append(append(setting, "\n\n"...), file...)
	}
	return os.WriteFile(path, file, 0o600)
}

// DefaultPath returns the default location of the configuration file.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".autoclaude", "config.toml")
}

// Load reads the configuration file.
//
// A mistake in one entry must not take the gateway down, and Claude Code
// with it. So a provider or a model that cannot be used is left out and
// named in warnings, and only a file that cannot be understood at all, or an
// address the gateway must not listen on, is an error.
func Load(path string) (cfg *Config, warnings []string, err error) {
	cfg = Default()
	meta, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return nil, nil, err
	}
	// The gateway forwards subscription credentials and decrypts traffic:
	// it must not be reachable from other machines.
	if host, _, err := net.SplitHostPort(cfg.Listen); err != nil || !isLoopback(host) {
		return nil, nil, fmt.Errorf("%s: listen must be a loopback address, got %q", path, cfg.Listen)
	}
	if cfg.Desktop != Slots && cfg.Desktop != Entries {
		warnings = append(warnings, fmt.Sprintf("desktop must be %q or %q, got %q; using %q", Slots, Entries, cfg.Desktop, Slots))
		cfg.Desktop = Slots
	}
	if cfg.ContextWindow < 0 {
		warnings = append(warnings, fmt.Sprintf("context_window must be a number of tokens, got %d; it is ignored", cfg.ContextWindow))
		cfg.ContextWindow = 0
	}
	for _, key := range meta.Undecoded() {
		warnings = append(warnings, fmt.Sprintf("unknown setting %q is ignored", key))
	}
	return cfg, append(warnings, cfg.prune()...), nil
}

// prune removes the providers and models that cannot be used and says why.
func (c *Config) prune() (warnings []string) {
	warn := func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}
	for name, p := range c.Providers {
		u, err := url.Parse(p.BaseURL)
		if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" || u.RawQuery != "" {
			warn("provider %q is ignored: base_url must be an http(s) URL without a query, got %q", name, p.BaseURL)
			delete(c.Providers, name)
		}
	}
	taken := map[string]bool{} // every ID the gateway already answers to
	usable := c.Models[:0]
	for _, m := range c.Models {
		switch _, hasProvider := c.Providers[m.Provider]; {
		case m.ID == "" || strings.ContainsAny(m.ID, " \t\n"):
			// The ID ends up inside thinking signatures, delimited by spaces.
			warn("model %q is ignored: the id must not be empty or contain whitespace", m.ID)
			continue
		case taken[m.ID]:
			warn("model %q is ignored: the id is already in use", m.ID)
			continue
		case !hasProvider:
			warn("model %q is ignored: it has no usable provider %q", m.ID, m.Provider)
			continue
		}
		taken[m.ID] = true
		if m.Upstream == "" {
			m.Upstream = m.ID
		}
		if m.Name == "" {
			m.Name = m.ID
		}
		if m.Context != 0 && (m.Context < MinContext || m.Context > MaxContext) {
			warn("model %q: context must be from %d to %d tokens, got %d; it is ignored", m.ID, MinContext, MaxContext, m.Context)
			m.Context = 0
		}
		if m.Slot != "" && taken[m.Slot] {
			warn("model %q: slot %q is already in use and is ignored", m.ID, m.Slot)
			m.Slot = ""
		}
		taken[m.Slot] = m.Slot != ""
		usable = append(usable, m)
	}
	c.Models = usable
	return warnings
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}
