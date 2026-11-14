package main

import (
	"context"
	"errors"
	"fmt"
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

// reloadPath is where a running gateway takes the order to re-read its
// configuration. `autoclaude reload` posts to it.
const reloadPath = "/autoclaude/reload"

// program is the gateway as the service manager runs it.
//
// It follows the configuration file without a restart, at two moments: when
// it is told to with `autoclaude reload`, and when Claude Desktop asks
// claude.ai for its menu, so that the menu is always built from the
// configuration the gateway is using.
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

// refresh re-reads the configuration file if it has changed since it was
// last read.
func (p *program) refresh() {
	if info, err := os.Stat(p.path); err == nil && !info.ModTime().Equal(p.lastModified()) {
		for _, line := range p.reload() {
			slog.Info(line)
		}
	}
}

func (p *program) lastModified() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.modified
}

// reload re-reads the configuration file and reports, line by line, what
// came of it. A file that cannot be used leaves the gateway as it is.
func (p *program) reload() (report []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if info, err := os.Stat(p.path); err == nil {
		p.modified = info.ModTime()
	}
	cfg, warnings, err := config.Load(p.path)
	if err != nil {
		return []string{"The configuration cannot be used and was not applied: " + err.Error()}
	}
	for _, warning := range warnings {
		report = append(report, "Warning: "+warning)
	}
	if cfg.Listen != p.cfg.Listen || cfg.Desktop != p.cfg.Desktop {
		report = append(report, "listen and desktop take effect with `autoclaude install`; the rest is applied now.")
		cfg.Listen, cfg.Desktop = p.cfg.Listen, p.cfg.Desktop
	}
	p.cfg = cfg
	p.gateway.Configure(cfg)
	if err := claudecode.SetModels(claudecode.SettingsPath(), pickerOptions(cfg)); err != nil {
		report = append(report, "Warning: the model picker of Claude Code was not updated: "+err.Error())
	}
	return append(report, modelLines(cfg)...)
}

// serveReload answers `autoclaude reload` with the report of the reload.
func (p *program) serveReload(w http.ResponseWriter, _ *http.Request) {
	for _, line := range p.reload() {
		slog.Info(line)
		fmt.Fprintln(w, line)
	}
}

// Start brings the gateway up and returns; the manager expects that.
func (p *program) Start(service.Service) error {
	cfg, dir := p.config(), filepath.Dir(p.path)
	ca, caPath, err := proxy.LoadCA(dir, apiCA, gateway.AnthropicHost)
	if err != nil {
		return err
	}
	direct := http.NewServeMux()
	direct.Handle("/", proxy.API(p.gateway))
	direct.Handle("POST "+reloadPath, proxy.Local(http.HandlerFunc(p.serveReload)))
	interceptions := []proxy.Interception{{Host: gateway.AnthropicHost, CA: ca, Handler: p.gateway}}
	if cfg.OwnEntries() {
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
	slog.Info("gateway started", "listen", cfg.Listen, "ca", caPath, "desktop", cfg.Desktop)
	for _, line := range modelLines(cfg) {
		slog.Info(line)
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
