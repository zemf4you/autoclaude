// Command autoclaude puts the user's own models into Claude Code and Claude
// Desktop: it runs a gateway between them and the model providers, and sets
// the user's machine up around that gateway.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zemf4you/autoclaude/internal/config"
	"github.com/zemf4you/autoclaude/internal/desktop"
	"github.com/zemf4you/autoclaude/internal/menu"
)

func main() {
	var path string // of the configuration file
	var yes bool    // do not ask

	// on returns what a command runs: an action on the user's setup.
	on := func(act func(*setup) error) func(*cobra.Command, []string) error {
		return func(*cobra.Command, []string) error {
			cfg, warnings, err := config.Load(path)
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("there is no configuration at %s: run `autoclaude install`", tilde(path))
			}
			if err != nil {
				return err
			}
			s, err := newSetup(path, cfg, warnings)
			if err != nil {
				return err
			}
			return act(s)
		}
	}
	// A user begins with install, or with run to try the gateway out: these
	// create the configuration file if there is none.
	starting := func(run func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			if err := create(path); err != nil {
				return err
			}
			return run(cmd, args)
		}
	}

	root := &cobra.Command{
		Use:   "autoclaude",
		Short: "Your own models in Claude Code and Claude Desktop",
		Long: "AutoClaude puts your own models into the official Claude Code and Claude\n" +
			"Desktop, next to Claude on your subscription.\n\n" +
			"Start with `autoclaude install`.",
		Version:       version(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&path, "config", config.DefaultPath(), "configuration file")
	root.CompletionOptions.HiddenDefaultCmd = true
	cobra.EnableCommandSorting = false

	install := &cobra.Command{
		Use:   "install",
		Short: "Set everything up; safe to run again",
		RunE:  starting(on(func(s *setup) error { return s.install(yes) })),
	}
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove everything install added",
		RunE:  on(func(s *setup) error { return s.uninstall(yes) }),
	}
	for _, c := range []*cobra.Command{install, uninstall} {
		c.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask; go by the configuration as it is")
	}
	root.AddCommand(
		install,
		&cobra.Command{
			Use:   "edit",
			Short: "Open the configuration in your editor, then apply it",
			RunE:  on((*setup).edit),
		},
		&cobra.Command{
			Use:   "reload",
			Short: "Apply an edited configuration, without a restart",
			RunE:  on((*setup).reload),
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show what is set up and whether your providers answer",
			RunE:  on((*setup).status),
		},
		uninstall,
		&cobra.Command{
			Use:   "run",
			Short: "Run the gateway in the foreground",
			RunE:  starting(func(*cobra.Command, []string) error { return serve(path) }),
		},
		&cobra.Command{
			Use:   "desktop",
			Short: "Start Claude Desktop with your models in its menu",
			RunE:  on(func(s *setup) error { return desktop.Launch(menu.PACScript(s.cfg.Listen)) }),
		},
	)
	for _, c := range root.Commands() {
		c.Args = cobra.NoArgs
	}

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr)
		message := err.Error()
		point(failure.Render("✗"), strings.ToUpper(message[:1])+message[1:])
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}
}

// create writes the configuration file a new user starts from, unless there
// is one already.
func create(path string) error {
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return config.Create(path)
}

// serve runs the gateway. A gateway that does not start takes Claude Code
// down with it, so on a configuration it cannot read it still starts, and
// passes everything to Anthropic.
func serve(path string) error {
	cfg, warnings, err := config.Load(path)
	if err != nil {
		slog.Error("configuration unusable, every model goes to Anthropic", "error", err)
		cfg = config.Default()
	}
	for _, warning := range warnings {
		slog.Warn(warning)
	}
	s, err := newSetup(path, cfg, warnings)
	if err != nil {
		return err
	}
	return s.daemon.Run()
}

// version returns the version of the module the command was built from.
func version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return "unknown"
}
