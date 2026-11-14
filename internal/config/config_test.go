package config

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func load(t *testing.T, content string) (*Config, error) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLoad(t *testing.T) {
	cfg, err := load(t, `
[providers.deepseek]
base_url = "https://api.deepseek.com/anthropic"
api_key = "sk-$ecret"

[providers.local]
base_url = "http://127.0.0.1:8000"

[[model]]
id = "deepseek-v4-pro"
provider = "deepseek"
menu = "DeepSeek V4 Pro"

[[model]]
id = "qwen3.5:4b"
provider = "local"
upstream = "Qwen/Qwen3-Coder"
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:8484" {
		t.Errorf("default listen = %q", cfg.Listen)
	}
	if got := cfg.Providers["deepseek"].APIKey; got != "sk-$ecret" {
		t.Errorf("api key = %q", got)
	}
	want := []Model{
		{ID: "deepseek-v4-pro", Provider: "deepseek", Upstream: "deepseek-v4-pro", Menu: "DeepSeek V4 Pro"},
		{ID: "qwen3.5:4b", Provider: "local", Upstream: "Qwen/Qwen3-Coder"},
	}
	if !slices.Equal(cfg.Models, want) {
		t.Errorf("models = %+v\nwant     %+v", cfg.Models, want)
	}
	if menu := cfg.Menu(); len(menu) != 1 || menu["deepseek-v4-pro"] != "DeepSeek V4 Pro" {
		t.Errorf("menu = %v", menu)
	}
}

// The example is what a new user starts from: it has to load as it is, and
// again with every example setting turned on.
func TestExample(t *testing.T) {
	if _, err := load(t, string(Example)); err != nil {
		t.Fatalf("as shipped: %v", err)
	}
	on := regexp.MustCompile(`(?m)^#([^ #\n])`).ReplaceAllString(string(Example), "$1")
	cfg, err := load(t, on)
	if err != nil {
		t.Fatalf("with the examples on: %v", err)
	}
	if len(cfg.Providers) != 3 || len(cfg.Models) != 2 || len(cfg.Menu()) != 2 {
		t.Errorf("providers %d, models %d, menu %d", len(cfg.Providers), len(cfg.Models), len(cfg.Menu()))
	}
}

func TestLoadRejectsMistakes(t *testing.T) {
	const provider = "[providers.local]\nbase_url = \"http://127.0.0.1:8000\"\n"
	model := func(id, extra string) string {
		return "[[model]]\nid = \"" + id + "\"\nprovider = \"local\"\n" + extra
	}
	cases := map[string]string{
		"unknown key":      `listn = ":1"`,
		"loopback":         `listen = "0.0.0.0:8484"`,
		"unknown provider": model("qwen", ""),
		"base_url":         "[providers.local]\nbase_url = \"localhost:8000\"",
		"whitespace":       provider + model("my model", ""),
		"listed twice":     provider + model("qwen", "") + model("qwen", ""),
		"cannot take":      provider + model("qwen", "menu = \"Qwen\"\n") + model("claude-opus-4-6", ""),
	}
	for want, content := range cases {
		if _, err := load(t, content); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error containing %q, got %v", want, err)
		}
	}
}
