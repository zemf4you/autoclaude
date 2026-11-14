package desktop

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// The proxy setting is delivered the way Anthropic documents it for macOS:
// as a configuration profile for Claude Desktop's preference domain. The
// profile belongs to the current user and holds that one setting.
const (
	profileID    = "io.github.zemf4you.autoclaude.desktop-proxy"
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
	<key>PayloadDisplayName</key><string>AutoClaude: model menu for Claude Desktop</string>
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

func profileInstalled() bool {
	out, _ := exec.Command("profiles", "list").CombinedOutput()
	return strings.Contains(string(out), profileID)
}

// setProxy hands the profile to the system. macOS installs profiles only
// with the user's consent in System Settings, so the pane is opened there.
func setProxy(pacURL string) error {
	if profileInstalled() {
		return nil
	}
	file, err := os.CreateTemp("", "autoclaude-*.mobileconfig")
	if err != nil {
		return err
	}
	defer file.Close()
	settings := struct{ ID, Setting, PACURL string }{profileID, proxySetting, pacURL}
	if err := profile.Execute(file, settings); err != nil {
		return err
	}
	if err := run("open", file.Name()); err != nil {
		return err
	}
	fmt.Println("One step is left to you: in System Settings, double-click the AutoClaude profile and choose Install.")
	return run("open", profilesPane)
}

func unsetProxy() error {
	if !profileInstalled() {
		return nil
	}
	return run("profiles", "remove", "-identifier", profileID)
}

func launch(flag string) error {
	return run("open", "-a", "Claude", "--args", flag)
}
