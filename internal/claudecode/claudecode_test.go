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

func TestSetModels(t *testing.T) {
	const original = `{"modelPicker":{"options":[{"model":"mine","label":"Mine"}],"replaceBuiltInOptions":false}}`
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	models := func() string {
		settings, _ := os.ReadFile(path)
		return gjson.GetBytes(settings, "modelPicker.options.#.model").String()
	}
	option := func(id string) Option {
		return Option{Model: id, Label: id, Description: Description("local")}
	}

	if err := SetModels(path, []Option{option("qwen"), option("glm")}); err != nil {
		t.Fatal(err)
	}
	if got := models(); got != `["mine","qwen","glm"]` {
		t.Errorf("after adding two: %s", got)
	}
	// A later set replaces ours and keeps the user's.
	if err := SetModels(path, []Option{option("glm")}); err != nil {
		t.Fatal(err)
	}
	if got := models(); got != `["mine","glm"]` {
		t.Errorf("after replacing: %s", got)
	}
	if err := SetModels(path, nil); err != nil {
		t.Fatal(err)
	}
	if settings, _ := os.ReadFile(path); models() != `["mine"]` || !gjson.GetBytes(settings, "modelPicker.replaceBuiltInOptions").Exists() {
		t.Errorf("after removing: %s", settings)
	}
}

func TestSetModelsLeavesNothingBehind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := SetModels(path, nil); err != nil {
		t.Errorf("nothing to remove and no file: %v", err)
	}
	os.WriteFile(path, []byte(`{"model":"opus"}`), 0o644)
	SetModels(path, []Option{{Model: "qwen", Label: "Qwen", Description: Description("local")}})
	SetModels(path, nil)
	if settings, _ := os.ReadFile(path); string(settings) != `{"model":"opus"}` {
		t.Errorf("settings = %s", settings)
	}
}
