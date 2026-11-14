// Package claudecode sets Claude Code up for the gateway, in the user's
// settings file.
//
// Claude Code reads its proxy and the certificates it trusts from the env
// block of that file, which covers the CLI and the Code sessions of Claude
// Desktop alike. Its model picker shows the models listed under modelPicker.
package claudecode

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/pretty"
	"github.com/tidwall/sjson"
)

// The variables Claude Code honours, as paths into the settings file.
const (
	proxyVariable = "env.HTTPS_PROXY"
	caVariable    = "env.NODE_EXTRA_CA_CERTS"
)

// SettingsPath returns the location of the user's Claude Code settings.
func SettingsPath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// Enable makes Claude Code use the proxy at proxyURL and trust the
// certificate authority at caPath. Everything else in the file is kept as it
// is, byte for byte.
func Enable(path, proxyURL, caPath string) error {
	settings, err := os.ReadFile(path)
	created := errors.Is(err, fs.ErrNotExist)
	if created {
		settings, err = []byte("{}"), os.MkdirAll(filepath.Dir(path), 0o755)
	}
	if err != nil {
		return err
	}
	if settings, err = sjson.SetBytes(settings, proxyVariable, proxyURL); err != nil {
		return err
	}
	if settings, err = sjson.SetBytes(settings, caVariable, caPath); err != nil {
		return err
	}
	if created {
		settings = pretty.Pretty(settings) // a file of our own may as well be readable
	}
	return os.WriteFile(path, settings, 0o644)
}

// Disable removes what Enable added and leaves the rest of the file alone.
func Disable(path string) error {
	settings, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, variable := range []string{proxyVariable, caVariable} {
		if settings, err = sjson.DeleteBytes(settings, variable); err != nil {
			return err
		}
	}
	if env := gjson.GetBytes(settings, "env"); env.IsObject() && len(env.Map()) == 0 {
		if settings, err = sjson.DeleteBytes(settings, "env"); err != nil {
			return err
		}
	}
	return os.WriteFile(path, settings, 0o644)
}

// Option is a model in the model picker of the CLI.
type Option struct {
	Model       string `json:"model"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// ours starts the description of the picker options this package manages.
// It is how they are told from the options the user wrote.
const ours = "AutoClaude"

// pickerPath is where the options of the model picker are kept.
const pickerPath = "modelPicker.options"

// Description returns the description of a picker option for a model served
// by provider.
func Description(provider string) string {
	return ours + ": " + provider
}

// SetModels makes the model picker of the CLI show options, next to whatever
// the user put there. Options set earlier are replaced; none removes them.
func SetModels(path string, options []Option) error {
	settings, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && len(options) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	kept := []json.RawMessage{}
	for _, option := range gjson.GetBytes(settings, pickerPath).Array() {
		if !strings.HasPrefix(option.Get("description").String(), ours) {
			kept = append(kept, json.RawMessage(option.Raw))
		}
	}
	for _, option := range options {
		raw, _ := json.Marshal(option)
		kept = append(kept, raw)
	}
	if len(kept) == 0 {
		settings, err = sjson.DeleteBytes(settings, pickerPath)
		if picker := gjson.GetBytes(settings, "modelPicker"); err == nil && picker.IsObject() && len(picker.Map()) == 0 {
			settings, err = sjson.DeleteBytes(settings, "modelPicker")
		}
	} else {
		settings, err = sjson.SetBytes(settings, pickerPath, kept)
	}
	if err != nil {
		return err
	}
	return os.WriteFile(path, settings, 0o644)
}

// Enabled reports whether Claude Code is pointed at the proxy at proxyURL.
func Enabled(path, proxyURL string) bool {
	settings, _ := os.ReadFile(path)
	return gjson.GetBytes(settings, proxyVariable).String() == proxyURL
}
