package desktop

import (
	"encoding/xml"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"text/template"
)

// The proxy setting is delivered the way Anthropic documents it for macOS:
// as a configuration profile for Claude Desktop's preference domain. The
// profile belongs to the current user and holds that one setting.
const (
	profileID    = "io.github.zemf4you.autoclaude.desktop-proxy"
	profileName  = "AutoClaude: model menu for Claude Desktop"
	profilesPane = "x-apple.systempreferences:com.apple.Profiles-Settings.extension"
)

// escape makes a value safe to put into the XML of the profile.
func escape(value string) string {
	var escaped strings.Builder
	xml.EscapeText(&escaped, []byte(value))
	return escaped.String()
}

var profile = template.Must(template.New("profile").Funcs(template.FuncMap{"xml": escape}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadType</key><string>Configuration</string>
	<key>PayloadVersion</key><integer>1</integer>
	<key>PayloadScope</key><string>User</string>
	<key>PayloadIdentifier</key><string>{{.ID}}</string>
	<key>PayloadUUID</key><string>6C0F4B1E-5D0A-4E0B-9A52-1F5B3A7C2D10</string>
	<key>PayloadDisplayName</key><string>{{.Name}}</string>
	<key>PayloadDescription</key><string>Sends the connections of Claude Desktop to claude.ai through the local AutoClaude gateway. Affects Claude Desktop only.</string>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>PayloadType</key><string>com.anthropic.claudefordesktop</string>
			<key>PayloadVersion</key><integer>1</integer>
			<key>PayloadIdentifier</key><string>{{.ID}}.settings</string>
			<key>PayloadUUID</key><string>B8A2D6E4-3C1F-4F7A-8E69-0D4C5B2A9F31</string>
			<key>{{.Setting}}</key><string>{{xml .PACURL}}</string>
		</dict>
	</array>
</dict>
</plist>
`))

func loginKeychain() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Keychains", "login.keychain-db")
}

func trusted(ca authority) bool {
	return succeeds("security", "verify-cert", "-c", ca.path, "-p", "ssl")
}

func trust(ca authority) error {
	return run("security", "add-trusted-cert", "-r", "trustRoot", "-p", "ssl", "-k", loginKeychain(), ca.path)
}

func untrust(ca authority) error {
	if err := run("security", "remove-trusted-cert", ca.path); err != nil {
		return err
	}
	return run("security", "delete-certificate", "-Z", ca.sha1, loginKeychain())
}

func proxyInstalled() bool {
	out, _ := exec.Command("profiles", "list").CombinedOutput()
	return strings.Contains(string(out), profileID)
}

// installedPAC returns the script address the installed profile carries.
// macOS keeps the settings of a user's profiles in a preferences file of
// that user's own.
func installedPAC() string {
	account, err := user.Current()
	if err != nil {
		return ""
	}
	preferences := filepath.Join("/Library/Managed Preferences", account.Username, "com.anthropic.claudefordesktop")
	out, _ := exec.Command("defaults", "read", preferences, proxySetting).Output()
	return strings.TrimSpace(string(out))
}

// setProxy hands the profile to the system. macOS installs profiles only
// with the user's consent in System Settings, so the pane is opened there.
func setProxy(pacURL string) error {
	if proxyInstalled() {
		if installedPAC() == pacURL {
			return nil
		}
		// The gateway has moved: the profile is replaced.
		if err := unsetProxy(); err != nil {
			return err
		}
	}
	file, err := os.CreateTemp("", "autoclaude-*.mobileconfig")
	if err != nil {
		return err
	}
	defer file.Close()
	settings := struct{ ID, Name, Setting, PACURL string }{profileID, profileName, proxySetting, pacURL}
	if err := profile.Execute(file, settings); err != nil {
		return err
	}
	if err := run("open", file.Name()); err != nil {
		return err
	}
	return run("open", profilesPane)
}

// byHandSteps is what installing the profile takes of the user.
var byHandSteps = []string{
	"In System Settings, double-click the AutoClaude profile.",
	"Click Install, and confirm with your password.",
}

func unsetProxy() error {
	if !proxyInstalled() {
		return nil
	}
	return run("profiles", "remove", "-identifier", profileID)
}

func launch(flag string) error {
	return run("open", "-a", "Claude", "--args", flag)
}
