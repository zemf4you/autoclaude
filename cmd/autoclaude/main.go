// Command autoclaude runs a gateway between Claude Code and model providers.
//
// Usage:
//
//	autoclaude [-config path]            run the gateway in the foreground
//	autoclaude [-config path] install    set everything up for this user
//	autoclaude [-config path] uninstall  undo install
//	                                     (both ask first; -y skips the question)
//	autoclaude [-config path] reload     apply the changes made to the config
//	autoclaude [-config path] status     say what is set up and what is not
//	autoclaude [-config path] desktop    start Claude Desktop with the menu
//
// Install registers the gateway as a service that starts at login and points
// Claude Code at it. With desktop = "entries", it also sets Claude Desktop up
// for the menu; desktop is the fallback where that is not possible.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/kardianos/service"

	"github.com/zemf4you/autoclaude/internal/claudecode"
	"github.com/zemf4you/autoclaude/internal/config"
	"github.com/zemf4you/autoclaude/internal/desktop"
	"github.com/zemf4you/autoclaude/internal/gateway"
	"github.com/zemf4you/autoclaude/internal/menu"
	"github.com/zemf4you/autoclaude/internal/proxy"
)

// The certificate authorities of the gateway, by the names of their files:
// one for the Anthropic API, one for the Desktop menu.
const (
	apiCA  = "ca"
	menuCA = "menu-ca"
)

// restartNotice follows a change of the setup: Claude Code and Claude Desktop
// read their settings once, when they start.
const restartNotice = "Restart Claude Code and Claude Desktop for the change to take effect."

func main() {
	path := flag.String("config", config.DefaultPath(), "path to the configuration file")
	assumeYes := flag.Bool("y", false, "do not ask before install and uninstall change anything")
	flag.Parse()

	if err := run(*path, flag.Arg(0), *assumeYes); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func run(path, command string, assumeYes bool) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if err := config.Create(path); err != nil {
			return err
		}
		fmt.Printf("Created %s.\nDescribe your providers and models there, then run `autoclaude install`.\n", path)
		return nil
	}
	cfg, warnings, err := config.Load(path)
	if err != nil {
		if command != "" {
			return err
		}
		// A gateway that does not start takes Claude Code down with it.
		// One that cannot read its configuration still passes everything
		// to Anthropic.
		slog.Error("configuration unusable, every model goes to Anthropic", "error", err)
		cfg = config.Default()
	}
	setup := &setup{path: path, dir: filepath.Dir(path), cfg: cfg, warnings: warnings}
	// The service belongs to the current user: it needs no administrator
	// rights where the system has per-user services, and it is restarted
	// if it ever stops.
	setup.daemon, err = service.New(newProgram(path, cfg), &service.Config{
		Name:        "autoclaude",
		DisplayName: "AutoClaude",
		Description: "Gateway between Claude Code and model providers.",
		Arguments:   []string{"-config", path},
		Option: service.KeyValue{
			"UserService":  true,
			"RunAtLoad":    true,
			"LogOutput":    true,
			"LogDirectory": setup.dir,
		},
	})
	if err != nil {
		return err
	}
	switch command {
	case "":
		for _, warning := range warnings {
			slog.Warn(warning)
		}
		return setup.daemon.Run()
	case "install":
		return setup.install(assumeYes)
	case "uninstall":
		return setup.uninstall(assumeYes)
	case "reload":
		return setup.reload()
	case "status":
		setup.status()
		return nil
	case "desktop":
		return desktop.Launch(menu.PACScript(cfg.Listen))
	}
	return fmt.Errorf("unknown command %q", command)
}

// setup is what autoclaude arranges on the user's machine around the gateway.
type setup struct {
	path string // configuration file
	dir  string // its directory, which also holds the certificates and logs
	cfg  *config.Config
	// warnings name the parts of the configuration that were left out.
	warnings []string
	daemon   service.Service
}

func (s *setup) proxyURL() string     { return "http://" + s.cfg.Listen }
func (s *setup) menuCertPath() string { return filepath.Join(s.dir, menuCA+".pem") }

// install brings the user's setup in line with the configuration. It says
// first what it is about to change and goes ahead only on a yes. It can be
// repeated whenever the configuration changes.
func (s *setup) install(assumeYes bool) error {
	settings := claudecode.SettingsPath()

	fmt.Println("autoclaude will:")
	fmt.Println("  1. Run the gateway as a service of your account that starts at login.")
	fmt.Printf("  2. Point Claude Code at it and list your models in its model picker,\n     in %s.\n", settings)
	if s.cfg.OwnEntries() {
		fmt.Println("  3. Give your models entries of their own in the menu of Claude Desktop,")
		fmt.Println("     as desktop = \"entries\" asks. For that it adds a certificate")
		fmt.Println("     for claude.ai to your account (your system will ask for your password)")
		fmt.Println("     and puts the gateway between Claude Desktop and claude.ai.")
		fmt.Println("     Anthropic does not support this: see \"Anthropic's terms\" in the README.")
	} else {
		fmt.Println("  3. Leave Claude Desktop with its standard menu; your models answer under")
		fmt.Println("     the Claude entries named by their slot. If a certificate and a proxy")
		fmt.Println("     setting are left from desktop = \"entries\", they are removed.")
	}
	s.printConfig()
	if !assumeYes && !confirmed("Continue?") {
		fmt.Println("Nothing was changed.")
		return nil
	}

	// A service installed earlier is replaced.
	s.daemon.Stop()
	s.daemon.Uninstall()
	if err := s.daemon.Install(); err != nil {
		return err
	}
	if err := s.daemon.Start(); err != nil {
		return err
	}
	// Claude Code is pointed at the gateway only once it is running.
	_, caPath, err := proxy.LoadCA(s.dir, apiCA, gateway.AnthropicHost)
	if err != nil {
		return err
	}
	if err := claudecode.Enable(settings, s.proxyURL(), caPath); err != nil {
		return err
	}
	if err := claudecode.SetModels(settings, pickerOptions(s.cfg)); err != nil {
		return err
	}
	if s.cfg.OwnEntries() {
		if _, _, err := proxy.LoadCA(s.dir, menuCA, menu.Host); err != nil {
			return err
		}
		err = desktop.Enable(s.menuCertPath(), s.proxyURL()+menu.PACPath)
	} else {
		err = desktop.Disable(s.menuCertPath())
	}
	if err != nil {
		return err
	}
	fmt.Println(restartNotice)
	return nil
}

// uninstall undoes install.
func (s *setup) uninstall(assumeYes bool) error {
	fmt.Println("autoclaude will stop its service, take its proxy and its models out of the")
	fmt.Println("Claude Code settings, and remove its certificate and Claude Desktop setting,")
	fmt.Println("if any.")
	if !assumeYes && !confirmed("Continue?") {
		fmt.Println("Nothing was changed.")
		return nil
	}
	// Claude Code lets go of the gateway before the gateway stops.
	settings := claudecode.SettingsPath()
	if err := claudecode.SetModels(settings, nil); err != nil {
		return err
	}
	if err := claudecode.Disable(settings); err != nil {
		return err
	}
	if err := desktop.Disable(s.menuCertPath()); err != nil {
		return err
	}
	s.daemon.Stop() // it may not be running
	if err := s.daemon.Uninstall(); err != nil {
		return err
	}
	fmt.Println(restartNotice)
	return nil
}

// status says what is set up and what is not, and what to do about it.
func (s *setup) status() {
	report := func(ok bool, good, bad string) {
		if ok {
			fmt.Println("  ok  " + good)
		} else {
			fmt.Println("  --  " + bad)
		}
	}
	fmt.Println("Config: " + s.path)

	fmt.Println("Setup:")
	state, _ := s.daemon.Status()
	report(state == service.StatusRunning && reachable(s.cfg.Listen),
		"the gateway is running on "+s.cfg.Listen,
		"the gateway is not running: run `autoclaude install`")
	report(claudecode.Enabled(claudecode.SettingsPath(), s.proxyURL()),
		"Claude Code uses the gateway",
		"Claude Code does not use the gateway: run `autoclaude install`")
	trusted, proxied := desktop.Status(s.menuCertPath())
	switch {
	case s.cfg.OwnEntries():
		report(trusted, "your account trusts the certificate for claude.ai",
			"your account does not trust the certificate for claude.ai: run `autoclaude install`")
		report(proxied, "Claude Desktop reaches claude.ai through the gateway",
			"Claude Desktop has no proxy setting: run `autoclaude install`, or start it with `autoclaude desktop`")
	case trusted || proxied:
		report(false, "", "desktop is \"slots\", but a certificate or proxy setting is still installed: run `autoclaude install`")
	default:
		report(true, "Claude Desktop keeps its standard menu (desktop = \"slots\")", "")
	}

	s.printConfig()
	fmt.Println("Providers:")
	for name, p := range s.cfg.Providers {
		report(reachable(hostPort(p.BaseURL)), "provider "+name+" is reachable", "provider "+name+" does not answer at "+p.BaseURL)
	}
}

// reload tells the running gateway to re-read the configuration and prints
// what the gateway made of it.
func (s *setup) reload() error {
	resp, err := http.Post(s.proxyURL()+reloadPath, "text/plain", nil)
	if err != nil {
		return fmt.Errorf("the gateway is not running: run `autoclaude install` (%w)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the gateway answered %s; it may be an older version: run `autoclaude install`", resp.Status)
	}
	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

// printConfig lists the configured models and what was left out of them.
func (s *setup) printConfig() {
	for _, warning := range s.warnings {
		fmt.Println("Warning: " + warning)
	}
	for _, line := range modelLines(s.cfg) {
		fmt.Println(line)
	}
}

// modelLines describes the configured models and how each can be picked in
// Claude Desktop.
func modelLines(cfg *config.Config) []string {
	lines := []string{"Models:"}
	for _, m := range cfg.Models {
		lines = append(lines, fmt.Sprintf("  %s, shown as %q, served by %s; in Claude Desktop: %s", m.ID, m.Name, m.Provider, inDesktop(cfg, m)))
	}
	if len(cfg.Models) == 0 {
		lines = append(lines, "  none: every request goes to Anthropic")
	}
	return lines
}

// inDesktop says how a model can be picked in Claude Desktop.
func inDesktop(cfg *config.Config, m config.Model) string {
	switch {
	case cfg.OwnEntries():
		return "its own entry"
	case m.Slot != "":
		return "in place of " + m.Slot
	}
	return "not shown, the model has no slot"
}

// pickerOptions returns the entries of the CLI's model picker for the models
// of a configuration.
func pickerOptions(cfg *config.Config) []claudecode.Option {
	options := make([]claudecode.Option, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		options = append(options, claudecode.Option{
			Model:       m.ID,
			Label:       m.Name,
			Description: claudecode.Description(m.Provider),
		})
	}
	return options
}

// confirmed asks a yes-or-no question on the terminal. Anything but a yes,
// the end of input included, is a no.
func confirmed(question string) bool {
	fmt.Print(question + " [y/N] ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	}
	return false
}
