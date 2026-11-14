package config

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func load(t *testing.T, content string) (*Config, []string, error) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

const twoModels = `
[providers.deepseek]
base_url = "https://api.deepseek.com/anthropic"
api_key = "sk-$ecret"

[providers.local]
base_url = "http://127.0.0.1:8000"

[[model]]
id = "deepseek-v4-pro"
provider = "deepseek"
name = "DeepSeek V4 Pro"
slot = "claude-opus-4-6"

[[model]]
id = "qwen3.5:4b"
provider = "local"
upstream = "Qwen/Qwen3-Coder"
`

func routes(cfg *Config) []string {
	return slices.Sorted(maps.Keys(cfg.Routes()))
}

func TestLoad(t *testing.T) {
	cfg, warnings, err := load(t, twoModels)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("warnings %q, error %v", warnings, err)
	}
	if cfg.Listen != "127.0.0.1:8484" {
		t.Errorf("default listen = %q", cfg.Listen)
	}
	if got := cfg.Providers["deepseek"].APIKey; got != "sk-$ecret" {
		t.Errorf("api key = %q", got)
	}
	want := []Model{
		{ID: "deepseek-v4-pro", Provider: "deepseek", Upstream: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", Slot: "claude-opus-4-6"},
		{ID: "qwen3.5:4b", Provider: "local", Upstream: "Qwen/Qwen3-Coder", Name: "qwen3.5:4b"},
	}
	if !slices.Equal(cfg.Models, want) {
		t.Errorf("models = %+v\nwant     %+v", cfg.Models, want)
	}
}

// The desktop setting picks the way into the menu of Claude Desktop.
func TestDesktopPicksTheWayIntoTheMenu(t *testing.T) {
	cfg, _, _ := load(t, twoModels)
	if got := routes(cfg); !slices.Equal(got, []string{"claude-opus-4-6", "deepseek-v4-pro", "qwen3.5:4b"}) {
		t.Errorf("with slots, the gateway answers to %q", got)
	}
	if len(cfg.Menu()) != 0 {
		t.Errorf("with slots, menu = %v", cfg.Menu())
	}

	cfg, _, _ = load(t, "desktop = \"entries\"\n"+twoModels)
	if got := routes(cfg); !slices.Equal(got, []string{"deepseek-v4-pro", "qwen3.5:4b"}) {
		t.Errorf("with the menu, the gateway answers to %q", got)
	}
	if menu := cfg.Menu(); len(menu) != 2 || menu["deepseek-v4-pro"] != "DeepSeek V4 Pro" || menu["qwen3.5:4b"] != "qwen3.5:4b" {
		t.Errorf("with the menu, menu = %v", menu)
	}
}

// The example is what a new user starts from: it has to load cleanly as it
// is, and again with every example setting turned on.
func TestExample(t *testing.T) {
	if _, warnings, err := load(t, string(Example)); err != nil || len(warnings) != 0 {
		t.Fatalf("as shipped: warnings %q, error %v", warnings, err)
	}
	on := regexp.MustCompile(`(?m)^#([^ #\n])`).ReplaceAllString(string(Example), "$1")
	cfg, warnings, err := load(t, on)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("with the examples on: warnings %q, error %v", warnings, err)
	}
	if len(cfg.Providers) != 3 || len(cfg.Models) != 2 || len(cfg.Routes()) != 4 || cfg.ContextWindow != 64000 {
		t.Errorf("providers %d, models %d, routes %d", len(cfg.Providers), len(cfg.Models), len(cfg.Routes()))
	}
}

// A mistake in one entry costs that entry, not the gateway.
func TestMistakesAreLeftOutWithAWarning(t *testing.T) {
	cfg, warnings, err := load(t, `
listn = ":1"
desktop = "menu"
context_window = -1

[providers.local]
base_url = "http://127.0.0.1:8000"

[providers.broken]
base_url = "localhost:8000"

[[model]]
id = "good"
provider = "local"
slot = "claude-opus-4-6"

[[model]]
id = "my model"
provider = "local"

[[model]]
id = "good"
provider = "local"

[[model]]
id = "orphan"
provider = "broken"

[[model]]
id = "second"
provider = "local"
slot = "claude-opus-4-6"
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := routes(cfg); !slices.Equal(got, []string{"claude-opus-4-6", "good", "second"}) {
		t.Errorf("the gateway answers to %q", got)
	}
	if cfg.Routes()["claude-opus-4-6"].ID != "good" {
		t.Error("a slot goes to the first model that names it")
	}
	for _, want := range []string{`desktop must be`, `context_window must be`, `"listn"`, `"broken"`, `"my model"`, `"good" is ignored`, `"orphan"`, `slot "claude-opus-4-6"`} {
		if !slices.ContainsFunc(warnings, func(w string) bool { return strings.Contains(w, want) }) {
			t.Errorf("no warning mentions %s in %q", want, warnings)
		}
	}
	if len(warnings) != 8 {
		t.Errorf("%d warnings, want 8: %q", len(warnings), warnings)
	}
	if cfg.OwnEntries() {
		t.Error("an unknown desktop setting must fall back to slots")
	}
}

func TestUnusableFileIsAnError(t *testing.T) {
	for name, content := range map[string]string{
		"not TOML":        `listen = `,
		"public address":  `listen = "0.0.0.0:8484"`,
		"missing address": `listen = "8484"`,
	} {
		if _, _, err := load(t, content); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
