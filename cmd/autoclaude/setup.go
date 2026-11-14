package main

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

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

// setup is what autoclaude arranges on the user's machine around the gateway.
type setup struct {
	path string // configuration file
	dir  string // its directory, which also holds the certificates and logs
	cfg  *config.Config
	// warnings name the parts of the configuration that were left out.
	warnings []string
	daemon   service.Service
}

// newSetup prepares the setup around the configuration at path.
func newSetup(path string, cfg *config.Config, warnings []string) (*setup, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	s := &setup{path: path, dir: filepath.Dir(path), cfg: cfg, warnings: warnings}
	// The service belongs to the current user: it needs no administrator
	// rights where the system has per-user services, and it is restarted
	// if it ever stops.
	s.daemon, err = service.New(newProgram(path, cfg), &service.Config{
		Name:        "autoclaude",
		DisplayName: "AutoClaude",
		Description: "Gateway between Claude Code and model providers.",
		Arguments:   []string{"run", "--config", path},
		Option: service.KeyValue{
			"UserService":  true,
			"RunAtLoad":    true,
			"LogOutput":    true,
			"LogDirectory": s.dir,
		},
	})
	return s, err
}

func (s *setup) proxyURL() string     { return "http://" + s.cfg.Listen }
func (s *setup) menuCertPath() string { return filepath.Join(s.dir, menuCA+".pem") }

// install brings the user's setup in line with the configuration. It asks
// how the models should appear in Claude Desktop, says what it is about to
// change and goes ahead only on a yes. It can be repeated at any time.
func (s *setup) install(yes bool) error {
	banner("install")
	// The stages are numbered, and one of them is a question.
	stage, stages := 0, 3
	if yes {
		stages = 2
	}
	next := func(title string) {
		stage++
		section(title, stage, stages)
	}
	configured := s.cfg.Desktop
	if !yes {
		next("Claude Desktop")
		desktop, answered, err := chooseDesktop(configured)
		if err != nil || !answered {
			return unchanged(err)
		}
		s.cfg.Desktop = desktop
	}

	settings := claudecode.SettingsPath()
	next("What will change")
	point("•", "A service of your account runs the gateway and starts at login.")
	point("•", "Claude Code is pointed at the gateway and lists your models, in "+tilde(settings)+".")
	if s.cfg.OwnEntries() {
		point("•", "Claude Desktop gets entries for your models: a certificate for claude.ai is added "+
			"to your account, and the gateway is put between the app and claude.ai.")
	} else {
		point("•", "Claude Desktop keeps its standard menu. A certificate and a proxy setting left "+
			"from entries of their own are removed.")
	}
	shellFile, shellLines, knownShell := completion()
	if knownShell {
		point("•", "Tab completion for `autoclaude` is added to "+tilde(shellFile)+".")
	}
	if len(s.cfg.Models) == 0 {
		point("•", "You have no models yet, so every request keeps going to Anthropic.")
	}
	for _, warning := range s.warnings {
		point(caution.Render("!"), warning)
	}
	if !yes {
		if ok, err := confirm("Go ahead?"); err != nil || !ok {
			return unchanged(err)
		}
	}

	next("Installing")
	if s.cfg.Desktop != configured {
		if err := config.SetDesktop(s.path, s.cfg.Desktop); err != nil {
			return err
		}
	}
	err := perform(
		step{"The gateway runs as a service of your account.", func() error {
			s.daemon.Stop() // a service installed earlier is replaced
			s.daemon.Uninstall()
			if err := s.daemon.Install(); err != nil {
				return err
			}
			return s.daemon.Start()
		}},
		// Claude Code is pointed at the gateway only once it is running.
		step{"Claude Code uses the gateway and lists your models.", func() error {
			_, ca, err := proxy.LoadCA(s.dir, apiCA, gateway.AnthropicHost)
			if err != nil {
				return err
			}
			if err := claudecode.Enable(settings, s.proxyURL(), ca); err != nil {
				return err
			}
			return applyModels(settings, s.cfg)
		}},
	)
	if err == nil {
		err = s.setUpDesktop(!yes)
	}
	if err == nil && knownShell {
		err = perform(step{"Tab completion works in new terminals.", func() error {
			return setBlock(shellFile, shellLines)
		}})
	}
	if err != nil {
		return err
	}

	if len(s.cfg.Models) > 0 {
		printReport(modelLines(s.cfg))
		callout(success, "Installed", restart, "Then pick one of your models in the model menu.")
		return nil
	}
	callout(success, "Installed", restart, "Then add your models: they go into "+tilde(s.path)+".")
	if !yes {
		if ok, err := confirm("Open the configuration now to add a model?"); err != nil || ok {
			return cmp.Or(err, s.edit())
		}
	}
	fmt.Println()
	say(1, "Run `autoclaude edit` when you are ready.")
	return nil
}

// setUpDesktop sets Claude Desktop up for the way into its menu that the
// configuration names. Where the system leaves a step to the user, it says
// which and, if it may, waits for them to take it.
func (s *setup) setUpDesktop(wait bool) error {
	if !s.cfg.OwnEntries() {
		if err := desktop.Disable(s.menuCertPath()); err != nil {
			return err
		}
		report(true, "Claude Desktop keeps its standard menu.", "")
		return nil
	}
	if _, _, err := proxy.LoadCA(s.dir, menuCA, menu.Host); err != nil {
		return err
	}
	if trusted, _ := desktop.Status(s.menuCertPath()); !trusted {
		point(caution.Render("→"), "Your system is asking for your password, to add the certificate for claude.ai.")
	}
	byHand, err := desktop.Enable(s.menuCertPath(), s.proxyURL()+menu.PACPath)
	if err != nil {
		return fmt.Errorf("Claude Desktop was not set up, the rest is in place (%w). "+
			"Run `autoclaude install` to try again or to choose the other way into its menu", err)
	}
	report(true, "Your account trusts the certificate for claude.ai.", "")
	proxied := func() bool {
		_, proxied := desktop.Status(s.menuCertPath())
		return proxied
	}
	if len(byHand) > 0 {
		for i, step := range byHand {
			byHand[i] = fmt.Sprintf("%d. %s", i+1, step)
		}
		callout(caution, "Your turn", byHand...)
		fmt.Println()
		if wait {
			waitFor("Waiting for you to finish (Ctrl+C to go on without it)", proxied)
		}
	}
	report(proxied(), "Claude Desktop reaches claude.ai through the gateway.",
		"Claude Desktop has no proxy setting yet: finish the step above, or start it with `autoclaude desktop`.")
	return nil
}

// edit opens the configuration for the user to edit and, once they are done
// with it, applies it.
func (s *setup) edit() error {
	finished, err := openEditor(s.path)
	if err != nil {
		return err
	}
	if !finished {
		fmt.Println()
		say(1, tilde(s.path)+" is open in your editor.", "Run `autoclaude reload` once you have saved it.")
		return nil
	}
	return s.reload()
}

// restart is what a change of the setup takes to show: Claude Code and
// Claude Desktop read their settings once, when they start.
const restart = "Restart Claude Code and Claude Desktop."

// uninstall undoes install.
func (s *setup) uninstall(yes bool) error {
	banner("uninstall")
	section("What will change", 1, 2)
	point("•", "Claude Code no longer goes through the gateway or lists your models.")
	point("•", "The certificate and the Claude Desktop setting are removed, if they were added.")
	point("•", "The service is stopped and removed.")
	shellFile, _, knownShell := completion()
	if knownShell {
		point("•", "Tab completion is taken out of "+tilde(shellFile)+".")
	}
	if !yes {
		if ok, err := confirm("Go ahead?"); err != nil || !ok {
			return unchanged(err)
		}
	}

	section("Removing", 2, 2)
	settings := claudecode.SettingsPath()
	err := perform(
		// Claude Code lets go of the gateway before the gateway stops.
		step{"Claude Code no longer uses the gateway.", func() error {
			if err := claudecode.SetModels(settings, nil); err != nil {
				return err
			}
			return claudecode.Disable(settings)
		}},
		step{"Claude Desktop is as it was.", func() error {
			return desktop.Disable(s.menuCertPath())
		}},
		step{"The service is removed.", func() error {
			s.daemon.Stop() // it may not be running
			return s.daemon.Uninstall()
		}},
		step{"Your shell is as it was.", func() error {
			if !knownShell {
				return nil
			}
			return setBlock(shellFile, nil)
		}},
	)
	if err != nil {
		return err
	}
	callout(success, "Removed", restart, "Your configuration is still in "+tilde(s.dir)+".")
	return nil
}

// status says what is set up and what is not, and what to do about it.
func (s *setup) status() error {
	banner("status")
	say(1, faint.Render(tilde(s.path)))

	section("Setup")
	state, _ := s.daemon.Status()
	report(state == service.StatusRunning && reachable(s.cfg.Listen),
		"The gateway is running on "+s.cfg.Listen+".",
		"The gateway is not running: run `autoclaude install`.")
	report(claudecode.Enabled(claudecode.SettingsPath(), s.proxyURL()),
		"Claude Code uses the gateway.",
		"Claude Code does not use the gateway: run `autoclaude install`.")
	trusted, proxied := desktop.Status(s.menuCertPath())
	switch {
	case s.cfg.OwnEntries():
		report(trusted, "Your account trusts the certificate for claude.ai.",
			"Your account does not trust the certificate for claude.ai: run `autoclaude install`.")
		report(proxied, "Claude Desktop reaches claude.ai through the gateway.",
			"Claude Desktop has no proxy setting: run `autoclaude install`, or start it with `autoclaude desktop`.")
	case trusted || proxied:
		report(false, "", "Your models take the place of Claude models, but a certificate or a proxy setting is still installed: run `autoclaude install`.")
	default:
		report(true, "Claude Desktop keeps its standard menu.", "")
	}

	s.printConfig()
	if len(s.cfg.Providers) > 0 {
		section("Providers")
		for _, name := range slices.Sorted(maps.Keys(s.cfg.Providers)) {
			address := s.cfg.Providers[name].BaseURL
			report(reachable(hostPort(address)), name+" is reachable.", name+" does not answer at "+address+".")
		}
	}
	fmt.Println()
	return nil
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
	report, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	banner("reload")
	printReport(strings.Split(strings.TrimRight(string(report), "\n"), "\n"))
	fmt.Println()
	return nil
}

// warningPrefix starts a line about something left out of the configuration.
const warningPrefix = "Warning: "

// printConfig lists the configured models and what was left out of them.
func (s *setup) printConfig() {
	lines := modelLines(s.cfg)
	for _, warning := range s.warnings {
		lines = append(lines, warningPrefix+warning)
	}
	printReport(lines)
}

// modelLines describes the configured models as a table under a heading.
func modelLines(cfg *config.Config) []string {
	if len(cfg.Models) == 0 {
		return []string{"Models", "  None yet: every request goes to Anthropic."}
	}
	var table strings.Builder
	columns := tabwriter.NewWriter(&table, 0, 0, 3, ' ', 0)
	fmt.Fprintln(columns, "  NAME\tID\tPROVIDER\tCONTEXT\tIN CLAUDE DESKTOP")
	for _, m := range cfg.Models {
		fmt.Fprintf(columns, "  %s\t%s\t%s\t%s\t%s\n", m.Name, m.ID, m.Provider, contextOf(cfg, m), inDesktop(cfg, m))
	}
	columns.Flush()
	return append([]string{"Models"}, strings.Split(strings.TrimRight(table.String(), "\n"), "\n")...)
}

// contextOf says what context window Claude Code assumes for a model.
func contextOf(cfg *config.Config, m config.Model) string {
	switch {
	case m.Context > 0:
		return strconv.Itoa(m.Context)
	case cfg.ContextWindow > 0:
		return strconv.Itoa(cfg.ContextWindow)
	}
	return "not set"
}

// inDesktop says how a model can be picked in Claude Desktop.
func inDesktop(cfg *config.Config, m config.Model) string {
	switch {
	case cfg.OwnEntries():
		return "its own entry"
	case m.Slot != "":
		return "in place of " + m.Slot
	}
	return "not shown: it has no slot"
}

// applyModels tells Claude Code about the models of a configuration: which
// to list in its model picker and what context window to assume for them.
func applyModels(settings string, cfg *config.Config) error {
	if err := claudecode.SetModels(settings, pickerOptions(cfg)); err != nil {
		return err
	}
	return claudecode.SetContextWindow(settings, cfg.ContextWindow)
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
