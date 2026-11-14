// Package claudecode points Claude Code at the gateway.
//
// Claude Code reads its proxy and the certificates it trusts from the env
// block of the user's settings file. That one place covers the CLI and the
// Code sessions of Claude Desktop alike.
package claudecode

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

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
