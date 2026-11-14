package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/zemf4you/autoclaude/internal/config"
)

// How the command's output looks.
//
// Everything stands off the left edge, so that it reads apart from the shell
// around it. A command opens with a banner, its parts have headings in the
// accent color, and what the user has to notice or do next is in a box.
// Colors are left out by themselves when the output is not a terminal.
var (
	accent  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	strong  = lipgloss.NewStyle().Bold(true)
	success = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	failure = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	caution = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	faint   = lipgloss.NewStyle().Faint(true)
	// paragraph wraps a longer text to a width that reads well.
	paragraph = lipgloss.NewStyle().Width(70)
	box       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 2).MarginLeft(2).Width(74)
)

// say prints lines at a depth of indentation: 1 for headings, 2 for what
// stands under them.
func say(depth int, lines ...string) {
	for _, line := range lines {
		fmt.Println(strings.Repeat("  ", depth) + line)
	}
}

// banner opens the output of a command.
func banner(command string) {
	fmt.Println()
	say(1, accent.Render("AutoClaude")+faint.Render(" · "+command))
}

// section starts a part of the output. A command that goes through stages
// numbers them, so that the user knows where they are.
func section(title string, stage ...int) {
	fmt.Println()
	if len(stage) == 2 {
		title = fmt.Sprintf("%d/%d  %s", stage[0], stage[1], title)
	}
	say(1, accent.Render(title))
}

// point prints a marked paragraph: an item of a list, or one line of a
// checklist.
func point(mark, text string) {
	fmt.Println(lipgloss.JoinHorizontal(lipgloss.Top, "    "+mark+" ", paragraph.Render(text)))
}

// report prints one line of a checklist: what holds, or what does not and
// what to do about it.
func report(ok bool, holds, fails string) {
	if ok {
		point(success.Render("✓"), holds)
	} else {
		point(failure.Render("✗"), fails)
	}
}

// callout puts what the user must not miss into a box of the given color.
func callout(color lipgloss.Style, title string, lines ...string) {
	text := color.Bold(true).Render(title) + "\n\n" + strings.Join(lines, "\n")
	fmt.Println()
	fmt.Println(box.BorderForeground(color.GetForeground()).Render(text))
}

// printReport prints a report of the gateway's configuration: warnings, and
// tables under their headings, with the titles of columns in capitals.
func printReport(lines []string) {
	for _, line := range lines {
		switch warning, isWarning := strings.CutPrefix(line, warningPrefix); {
		case isWarning:
			point(caution.Render("!"), warning)
		case !strings.HasPrefix(line, " "):
			section(line)
		case line == strings.ToUpper(line):
			say(1, faint.Render(line))
		default:
			say(1, line)
		}
	}
}

// tilde shortens a path in the user's home directory for display.
func tilde(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// A step is one thing a command does to the user's machine, named by what
// holds once it is done.
type step struct {
	done string
	do   func() error
}

// perform takes the steps in order, ticking off each, and stops at the first
// that fails.
func perform(steps ...step) error {
	for _, step := range steps {
		if err := step.do(); err != nil {
			return err
		}
		report(true, step.done, "")
	}
	return nil
}

// unchanged ends a command the user backed out of.
func unchanged(err error) error {
	if err == nil {
		say(1, faint.Render("Nothing was changed."))
	}
	return err
}

// errNotATerminal is returned where a question has nobody to answer it.
var errNotATerminal = errors.New("this is not a terminal, so nobody can be asked: pass --yes to go by the configuration as it is")

// accessible says the user asked for plain prompts, the way screen readers
// need them.
func accessible() bool { return os.Getenv("ACCESSIBLE") != "" }

// ask puts one question to the user. It reports false if the user backed out.
func ask(field huh.Field) (answered bool, err error) {
	if !accessible() && !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, errNotATerminal
	}
	keys := huh.NewDefaultKeyMap()
	keys.Select.Filter.SetEnabled(false) // a handful of choices needs no search
	theme := huh.ThemeBase16()
	theme.Focused.Base = theme.Focused.Base.MarginLeft(2)
	fmt.Println()
	err = huh.NewForm(huh.NewGroup(field)).WithTheme(theme).WithKeyMap(keys).WithAccessible(accessible()).Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return false, nil
	}
	return err == nil, err
}

// confirm asks a yes-or-no question; backing out is a no.
func confirm(question string) (bool, error) {
	var yes bool
	answered, err := ask(huh.NewConfirm().Title(question).Affirmative("Yes").Negative("No").Value(&yes))
	return answered && yes, err
}

// The two ways into the menu of Claude Desktop, as the user is told of them:
// what each is good for, and what it costs.
var desktopChoices = []struct{ desktop, name, gain, cost string }{
	{config.Slots, "In place of Claude models",
		"Nothing more to install, and the lower risk of the two.",
		"Feels like a workaround: you give your model the place of a Claude model you do not use, " +
			"then pick, say, Opus 4.6 in the menu, and your model answers."},
	{config.Entries, "As entries of their own",
		"Looks and feels native: your models are in the menu under their own names.",
		"The higher risk: it changes what Anthropic's service sends to the app, which Anthropic " +
			"does not support and which may cost you your account. Read \"Anthropic's terms\" in the README first."},
}

// chooseDesktop asks how the models should reach the menu of Claude Desktop,
// starting from the current way. It reports false if the user backed out.
func chooseDesktop(current string) (desktop string, answered bool, err error) {
	var options []huh.Option[string]
	names := map[string]string{}
	for _, choice := range desktopChoices {
		fmt.Println()
		say(2, strong.Render(choice.name))
		point(success.Render("+"), choice.gain)
		point(caution.Render("−"), choice.cost)
		options = append(options, huh.NewOption(choice.name, choice.desktop))
		names[choice.desktop] = choice.name
	}
	desktop = current
	answered, err = ask(huh.NewSelect[string]().
		Title("How should your models appear in Claude Desktop?").
		Options(options...).
		Value(&desktop))
	if answered {
		point(accent.Render("→"), names[desktop])
	}
	return desktop, answered, err
}

// openEditor opens a text file for the user to edit. With an editor of the
// terminal, named by VISUAL or EDITOR, it returns when the user is finished;
// otherwise it hands the file to the system's text editor and returns at once.
func openEditor(path string) (finished bool, err error) {
	if editor := strings.Fields(cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"))); len(editor) > 0 {
		cmd := exec.Command(editor[0], append(editor[1:], path)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return true, cmd.Run()
	}
	switch runtime.GOOS {
	case "darwin":
		return false, exec.Command("open", "-t", path).Run()
	case "windows":
		return false, exec.Command("notepad", path).Start()
	}
	return false, exec.Command("xdg-open", path).Run()
}

// waitFor shows that the command is waiting until done holds or the user
// interrupts it.
func waitFor(what string, done func() bool) {
	spinner.New().Title(" " + what).Action(func() {
		for !done() {
			time.Sleep(time.Second)
		}
	}).Run() // an interruption only ends the wait
}
