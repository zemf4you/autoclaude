package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kardianos/service"

	"github.com/zemf4you/autoclaude/internal/claudecode"
	"github.com/zemf4you/autoclaude/internal/config"
	"github.com/zemf4you/autoclaude/internal/gateway"
	"github.com/zemf4you/autoclaude/internal/menu"
	"github.com/zemf4you/autoclaude/internal/proxy"
)

// sessionStart is the request every Claude Code session makes when it starts.
const sessionStart = "/api/claude_cli/bootstrap"

// program is the gateway as the service manager runs it.
//
// It follows the configuration file without a restart, but only at the
// moments when Claude builds its list of models: when Desktop asks claude.ai
// for its menu and, where that menu is not ours, when a Claude Code session
// starts. Picking the changes up at any other time could leave Claude
// offering a model the gateway has just stopped serving.
type program struct {
	path    string
	gateway *gateway.Gateway
	server  *http.Server

	mu       sync.Mutex
	cfg      *config.Config
	modified time.Time // of the configuration file, when it was last read
}

func newProgram(path string, cfg *config.Config) *program {
	p := &program{path: path, gateway: gateway.New(cfg), cfg: cfg}
	if info, err := os.Stat(path); err == nil {
		p.modified = info.ModTime()
	}
	return p
}

func (p *program) config() *config.Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg
}

// refresh re-reads the configuration file if it has changed. A file that
// cannot be used is reported and the gateway keeps what it has.
func (p *program) refresh() {
	p.mu.Lock()
	defer p.mu.Unlock()
	info, err := os.Stat(p.path)
	if err != nil || info.ModTime().Equal(p.modified) {
		return
	}
	p.modified = info.ModTime()
	cfg, warnings, err := config.Load(p.path)
	if err != nil {
		slog.Error("configuration changed but cannot be used, keeping the previous one", "error", err)
		return
	}
	for _, warning := range warnings {
		slog.Warn(warning)
	}
	if cfg.Listen != p.cfg.Listen || cfg.DesktopMenu != p.cfg.DesktopMenu {
		slog.Warn("listen and desktop_menu take effect with `autoclaude install`; the rest of the configuration is applied now")
		cfg.Listen, cfg.DesktopMenu = p.cfg.Listen, p.cfg.DesktopMenu
	}
	p.cfg = cfg
	p.gateway.Configure(cfg)
	if err := claudecode.SetModels(claudecode.SettingsPath(), pickerOptions(cfg)); err != nil {
		slog.Warn("the model picker of Claude Code was not updated", "error", err)
	}
	slog.Info("configuration reloaded")
	logModels(cfg)
}

func logModels(cfg *config.Config) {
	for _, m := range cfg.Models {
		slog.Info("model", "id", m.ID, "name", m.Name, "provider", m.Provider, "upstream", m.Upstream, "in Desktop", inDesktop(cfg, m))
	}
}

// RoundTrip passes API requests to the gateway.
func (p *program) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == sessionStart && !p.config().DesktopMenu {
		p.refresh()
	}
	return p.gateway.RoundTrip(r)
}

// Start brings the gateway up and returns; the manager expects that.
func (p *program) Start(service.Service) error {
	cfg, dir := p.config(), filepath.Dir(p.path)
	ca, caPath, err := proxy.LoadCA(dir, apiCA, gateway.AnthropicHost)
	if err != nil {
		return err
	}
	direct := http.NewServeMux()
	direct.Handle("/", proxy.API(p))
	interceptions := []proxy.Interception{{Host: gateway.AnthropicHost, CA: ca, Handler: p}}
	if cfg.DesktopMenu {
		ca, _, err := proxy.LoadCA(dir, menuCA, menu.Host)
		if err != nil {
			return err
		}
		direct.Handle("GET "+menu.PACPath, menu.PAC(cfg.Listen))
		interceptions = append(interceptions, proxy.Interception{
			Host: menu.Host,
			CA:   ca,
			Handler: &menu.Menu{
				Models:    func() map[string]string { return p.config().Menu() },
				Refresh:   p.refresh,
				Transport: gateway.DirectTransport(),
			},
		})
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	slog.Info("gateway started", "listen", cfg.Listen, "ca", caPath, "desktop menu", cfg.DesktopMenu)
	logModels(cfg)
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

// Stop shuts the gateway down, giving requests in flight a moment to finish.
func (p *program) Stop(service.Service) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.server.Shutdown(ctx)
}

// reachable reports whether something accepts connections at address.
func reachable(address string) bool {
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// hostPort returns the address a base URL is served at.
func hostPort(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	if u.Port() != "" {
		return u.Host
	}
	if u.Scheme == "https" {
		return net.JoinHostPort(u.Hostname(), "443")
	}
	return net.JoinHostPort(u.Hostname(), "80")
}
