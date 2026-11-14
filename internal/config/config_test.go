package config

import (
	"os"
	"path/filepath"
	"regexp"
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

[models]
deepseek-v4-pro = "deepseek"
"claude-haiku-5.5" = "local/Qwen/Qwen3-Coder"
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
	want := map[string]Route{
		"deepseek-v4-pro":  {Provider: "deepseek", Model: "deepseek-v4-pro"},
		"claude-haiku-5.5": {Provider: "local", Model: "Qwen/Qwen3-Coder"},
	}
	for model, route := range want {
		if cfg.Models[model] != route {
			t.Errorf("route of %q = %+v, want %+v", model, cfg.Models[model], route)
		}
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
	if len(cfg.Providers) != 3 || len(cfg.Models) != 3 || len(cfg.Menu) != 2 {
		t.Errorf("providers %d, models %d, menu %d", len(cfg.Providers), len(cfg.Models), len(cfg.Menu))
	}
}

func TestLoadRejectsMistakes(t *testing.T) {
	const provider = "[providers.local]\nbase_url = \"http://127.0.0.1:8000\"\n"
	cases := map[string]string{
		"unknown key":      `listn = ":1"`,
		"loopback":         `listen = "0.0.0.0:8484"`,
		"unknown provider": "[models]\nqwen = \"local\"",
		"base_url":         "[providers.local]\nbase_url = \"localhost:8000\"",
		"whitespace":       provider + "[models]\n\"my model\" = \"local\"",
		"not listed":       provider + "[menu]\nqwen = \"Qwen\"",
		"cannot take":      provider + "[models]\nqwen = \"local\"\nclaude-opus-4-6 = \"local\"\n[menu]\nqwen = \"Qwen\"",
	}
	for want, content := range cases {
		if _, err := load(t, content); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error containing %q, got %v", want, err)
		}
	}
}
