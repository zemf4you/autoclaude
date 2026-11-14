package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// managedSettings is where Claude Desktop reads its settings on Linux. The
// file is system-wide, so writing it takes sudo.
const managedSettings = "/etc/claude-desktop/managed-settings.json"

// nssDatabase is the per-user certificate store that Chromium reads.
func nssDatabase() string {
	home, _ := os.UserHomeDir()
	return "sql:" + filepath.Join(home, ".pki", "nssdb")
}

func trusted(ca authority) bool {
	return succeeds("certutil", "-d", nssDatabase(), "-L", "-n", ca.name)
}

func trust(ca authority) error {
	return run("certutil", "-d", nssDatabase(), "-A", "-t", "C,,", "-n", ca.name, "-i", ca.path)
}

func untrust(ca authority) error {
	return run("certutil", "-d", nssDatabase(), "-D", "-n", ca.name)
}

func setProxy(pacURL string) error {
	return writeSettings(func(settings map[string]any) { settings[proxySetting] = pacURL })
}

func unsetProxy() error {
	return writeSettings(func(settings map[string]any) { delete(settings, proxySetting) })
}

// writeSettings applies change to the managed settings, keeping every other
// setting the file holds.
func writeSettings(change func(map[string]any)) error {
	settings := map[string]any{}
	data, err := os.ReadFile(managedSettings)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("%s: %w", managedSettings, err)
		}
	}
	change(settings)
	if len(settings) == 0 {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return run("sudo", "rm", managedSettings)
	}
	file, err := os.CreateTemp("", "autoclaude-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(settings); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return run("sudo", "install", "-D", "-m", "644", file.Name(), managedSettings)
}

func launch(flag string) error {
	return run("claude-desktop", flag)
}
