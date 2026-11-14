package main

import (
	"bytes"
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Tab completion comes from the command itself: `autoclaude completion
// <shell>` prints the script. Install has the user's shell load it at start,
// with a few lines in the shell's own startup file, between marks that let
// uninstall find them again.
const (
	blockStart = "# >>> autoclaude completion >>>"
	blockEnd   = "# <<< autoclaude completion <<<"
)

var block = regexp.MustCompile(`(?s)\n*` + regexp.QuoteMeta(blockStart) + `.*?` + regexp.QuoteMeta(blockEnd) + `\n?`)

// completion returns the startup file of the user's shell and the lines that
// load the completion there. It reports false for a shell it does not know.
func completion() (file string, lines []string, known bool) {
	home, _ := os.UserHomeDir()
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		return filepath.Join(cmp.Or(os.Getenv("ZDOTDIR"), home), ".zshrc"), []string{
			"if command -v autoclaude >/dev/null; then",
			"  (( $+functions[compdef] )) || { autoload -Uz compinit && compinit; }",
			"  source <(autoclaude completion zsh)",
			"fi",
		}, true
	case "bash":
		return filepath.Join(home, ".bashrc"), []string{
			"if command -v autoclaude >/dev/null; then",
			"  source <(autoclaude completion bash)",
			"fi",
		}, true
	case "fish":
		return filepath.Join(home, ".config", "fish", "conf.d", "autoclaude.fish"), []string{
			"command -q autoclaude; and autoclaude completion fish | source",
		}, true
	}
	return "", nil, false
}

// setBlock puts the lines into the file between the marks, in place of what
// was between them before. Without lines it takes the block out; a file left
// empty by that is removed.
func setBlock(file string, lines []string) error {
	content, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	content = block.ReplaceAllLiteral(content, []byte("\n"))
	if len(lines) > 0 {
		content = bytes.TrimRight(content, "\n")
		if len(content) > 0 {
			content = append(content, "\n\n"...)
		}
		content = append(content, blockStart+"\n"+strings.Join(lines, "\n")+"\n"+blockEnd+"\n"...)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(file); err == nil {
		mode = info.Mode() // the file stays as private as the user made it
	}
	return os.WriteFile(file, content, mode)
}
