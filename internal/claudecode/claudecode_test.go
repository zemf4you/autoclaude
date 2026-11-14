package claudecode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tidwall/gjson"
)

func TestEnableAndDisable(t *testing.T) {
	const original = "{\n  \"model\": \"opus\",\n  \"env\": {\n    \"FOO\": \"bar\"\n  }\n}\n"
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Enable(path, "http://127.0.0.1:8484", "/home/u/.autoclaude/ca.pem"); err != nil {
		t.Fatal(err)
	}
	settings, _ := os.ReadFile(path)
	for key, want := range map[string]string{
		"env.HTTPS_PROXY":         "http://127.0.0.1:8484",
		"env.NODE_EXTRA_CA_CERTS": "/home/u/.autoclaude/ca.pem",
		"env.FOO":                 "bar",
		"model":                   "opus",
	} {
		if got := gjson.GetBytes(settings, key).String(); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	if err := Disable(path); err != nil {
		t.Fatal(err)
	}
	if settings, _ = os.ReadFile(path); string(settings) != original {
		t.Errorf("after Disable the file is not what it was:\n%s", settings)
	}
}

func TestEnableCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	if err := Enable(path, "http://127.0.0.1:8484", "/ca.pem"); err != nil {
		t.Fatal(err)
	}
	if err := Disable(path); err != nil {
		t.Fatal(err)
	}
	if settings, _ := os.ReadFile(path); gjson.GetBytes(settings, "env").Exists() || !gjson.ValidBytes(settings) {
		t.Errorf("after Disable: %s", settings)
	}
	if err := Disable(filepath.Join(t.TempDir(), "missing.json")); err != nil {
		t.Errorf("Disable without a file: %v", err)
	}
}
