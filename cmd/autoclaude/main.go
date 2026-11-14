// Command autoclaude runs a gateway between Claude Code and model providers.
//
// Usage:
//
//	autoclaude [-config path]            run the gateway in the foreground
//	autoclaude [-config path] install    set everything up for this user
//	autoclaude [-config path] uninstall  undo install
//	                                     (both ask first; -y skips the question)
//	autoclaude [-config path] desktop    start Claude Desktop with the menu
//
// Install registers the gateway as a service that starts at login and points
// Claude Code at it. With desktop_menu on, it also sets Claude Desktop up
// for the menu; desktop is the fallback where that is not possible.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	for _, warning := range warnings {
		slog.Warn(warning)
	}
	dir := filepath.Dir(path)
	// The service belongs to the current user: it needs no administrator
	// rights where the system has per-user services, and it is restarted
	// if it ever stops.
	daemon, err := service.New(&program{cfg: cfg, dir: dir}, &service.Config{
		Name:        "autoclaude",
		DisplayName: "AutoClaude",
		Description: "Gateway between Claude Code and model providers.",
		Arguments:   []string{"-config", path},
		Option: service.KeyValue{
			"UserService":  true,
			"RunAtLoad":    true,
			"LogOutput":    true,
			"LogDirectory": dir,
		},
	})
	if err != nil {
		return err
	}
	switch command {
	case "":
		return daemon.Run()
	case "install":
		return install(cfg, dir, daemon, assumeYes)
	case "uninstall":
		return uninstall(dir, daemon, assumeYes)
	case "desktop":
		return desktop.Launch(menu.PACScript(cfg.Listen))
	}
	return fmt.Errorf("unknown command %q", command)
}

// install brings the user's setup in line with the configuration. It says
// first what it is about to change and goes ahead only on a yes. It can be
// repeated whenever the configuration changes.
func install(cfg *config.Config, dir string, daemon service.Service, assumeYes bool) error {
	settings := claudecode.SettingsPath()
	withMenu := cfg.DesktopMenu

	fmt.Println("autoclaude will:")
	fmt.Println("  1. Run the gateway as a service of your account that starts at login.")
	fmt.Printf("  2. Point Claude Code at it, in %s.\n", settings)
	if withMenu {
		fmt.Println("  3. Give your models entries of their own in the menu of Claude Desktop,")
		fmt.Println("     as desktop_menu in your config asks. For that it adds a certificate")
		fmt.Println("     for claude.ai to your account (your system will ask for your password)")
		fmt.Println("     and puts the gateway between Claude Desktop and claude.ai.")
		fmt.Println("     Anthropic does not support this: see \"Anthropic's terms\" in the README.")
	} else {
		fmt.Println("  3. Leave Claude Desktop with its standard menu; your models answer under")
		fmt.Println("     the Claude entries named by their slot. If a certificate and a proxy")
		fmt.Println("     setting are left from desktop_menu, they are removed.")
	}
	fmt.Println("Your models:")
	for _, m := range cfg.Models {
		fmt.Printf("  %s, served by %s; in Claude Desktop: %s\n", m.ID, m.Provider, inDesktop(cfg, m))
	}
	if len(cfg.Models) == 0 {
		fmt.Println("  none: every request goes to Anthropic")
	}
	if !assumeYes && !confirmed("Continue?") {
		fmt.Println("Nothing was changed.")
		return nil
	}

	// A service installed earlier is replaced.
	daemon.Stop()
	daemon.Uninstall()
	if err := daemon.Install(); err != nil {
		return err
	}
	if err := daemon.Start(); err != nil {
		return err
	}
	// Claude Code is pointed at the gateway only once it is running.
	_, caPath, err := proxy.LoadCA(dir, apiCA, gateway.AnthropicHost)
	if err != nil {
		return err
	}
	if err := claudecode.Enable(settings, "http://"+cfg.Listen, caPath); err != nil {
		return err
	}
	menuCertPath := filepath.Join(dir, menuCA+".pem")
	if withMenu {
		if _, _, err := proxy.LoadCA(dir, menuCA, menu.Host); err != nil {
			return err
		}
		err = desktop.Enable(menuCertPath, "http://"+cfg.Listen+menu.PACPath)
	} else {
		err = desktop.Disable(menuCertPath)
	}
	if err != nil {
		return err
	}
	fmt.Println(restartNotice)
	return nil
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

// uninstall undoes install.
func uninstall(dir string, daemon service.Service, assumeYes bool) error {
	fmt.Println("autoclaude will stop its service, take its proxy out of the Claude Code")
	fmt.Println("settings, and remove its certificate and Claude Desktop setting, if any.")
	if !assumeYes && !confirmed("Continue?") {
		fmt.Println("Nothing was changed.")
		return nil
	}
	// Claude Code lets go of the gateway before the gateway stops.
	if err := claudecode.Disable(claudecode.SettingsPath()); err != nil {
		return err
	}
	if err := desktop.Disable(filepath.Join(dir, menuCA+".pem")); err != nil {
		return err
	}
	daemon.Stop() // it may not be running
	if err := daemon.Uninstall(); err != nil {
		return err
	}
	fmt.Println(restartNotice)
	return nil
}

// program is the gateway as the service manager runs it.
type program struct {
	cfg    *config.Config
	dir    string
	server *http.Server
}

// Start brings the gateway up and returns; the manager expects that.
func (p *program) Start(service.Service) error {
	api := gateway.New(p.cfg)
	ca, caPath, err := proxy.LoadCA(p.dir, apiCA, gateway.AnthropicHost)
	if err != nil {
		return err
	}
	direct := http.NewServeMux()
	direct.Handle("/", proxy.API(api))
	interceptions := []proxy.Interception{{Host: gateway.AnthropicHost, CA: ca, Handler: api}}
	if p.cfg.DesktopMenu {
		ca, _, err := proxy.LoadCA(p.dir, menuCA, menu.Host)
		if err != nil {
			return err
		}
		direct.Handle("GET "+menu.PACPath, menu.PAC(p.cfg.Listen))
		interceptions = append(interceptions, proxy.Interception{
			Host:    menu.Host,
			CA:      ca,
			Handler: &menu.Menu{Models: p.cfg.Menu(), Transport: gateway.DirectTransport()},
		})
	}
	listener, err := net.Listen("tcp", p.cfg.Listen)
	if err != nil {
		return err
	}
	slog.Info("gateway started", "listen", p.cfg.Listen, "ca", caPath, "desktop menu", p.cfg.DesktopMenu)
	for _, m := range p.cfg.Models {
		slog.Info("model", "id", m.ID, "provider", m.Provider, "upstream", m.Upstream, "in Desktop", inDesktop(p.cfg, m))
	}
	p.server = &http.Server{
		Handler:           proxy.New(direct, interceptions...),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := p.server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			slog.Error(err.Error())
			os.Exit(1)
		}
	}()
	return nil
}

// inDesktop says how a model can be picked in Claude Desktop.
func inDesktop(cfg *config.Config, m config.Model) string {
	switch {
	case cfg.DesktopMenu && m.Menu != "":
		return fmt.Sprintf("menu entry %q", m.Menu)
	case !cfg.DesktopMenu && m.Slot != "":
		return "in place of " + m.Slot
	case cfg.DesktopMenu:
		return "not shown: the model has no menu name"
	}
	return "not shown: the model has no slot"
}

// Stop shuts the gateway down, giving requests in flight a moment to finish.
func (p *program) Stop(service.Service) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.server.Shutdown(ctx)
}
