package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The lines for the shell go into the user's own startup file: nothing else
// in it may change, and uninstall has to leave it as it found it.
func TestSetBlock(t *testing.T) {
	file := filepath.Join(t.TempDir(), "conf", ".zshrc")
	read := func() string {
		content, _ := os.ReadFile(file)
		return string(content)
	}

	if err := setBlock(file, []string{"first"}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != blockStart+"\nfirst\n"+blockEnd+"\n" {
		t.Errorf("a new file = %q", got)
	}
	if err := setBlock(file, nil); err != nil || read() != "" {
		t.Errorf("a file that held only the block must be removed: %q, %v", read(), err)
	}

	const mine = "export EDITOR=vim\n\nalias ll='ls -l'\n"
	os.WriteFile(file, []byte(mine), 0o600)
	for _, lines := range [][]string{{"first"}, {"second", "third"}} {
		if err := setBlock(file, lines); err != nil {
			t.Fatal(err)
		}
	}
	if got := read(); !strings.HasPrefix(got, mine) || strings.Count(got, blockStart) != 1 || strings.Contains(got, "first") || !strings.Contains(got, "second\nthird") {
		t.Errorf("the block must be replaced, and the rest kept: %q", got)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Errorf("the file became %v", info.Mode().Perm())
	}
	if err := setBlock(file, nil); err != nil || read() != mine {
		t.Errorf("without the block the file must be as it was: %q, %v", read(), err)
	}
}
