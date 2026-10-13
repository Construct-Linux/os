package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A pipeline whose later program cannot start fails, and only once the programs it started have
// ended: none is left writing after it returns.
func TestPipelineWaitsWhenAProgramCannotStart(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ended")
	err := pipeline(dir, filepath.Join(dir, "out"), nil,
		[]string{"sh", "-c", "sleep 0.2; touch " + marker},
		[]string{filepath.Join(dir, "no-such-program")})
	if err == nil || !strings.Contains(err.Error(), "no-such-program") {
		t.Errorf("pipeline = %v, want no-such-program's error", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("pipeline returned before the program it started ended: %v", err)
	}
}

// A command that fails says why on stderr, and its error carries it: an exit status alone does
// not. What it printed on stdout is the answer, and stderr stays out of it.
func TestOutputSaysWhy(t *testing.T) {
	got, err := output(exec.Command("sh", "-c", "echo answer; echo noise >&2"))
	if err != nil || got != "answer" {
		t.Errorf("output = %q, %v; want answer", got, err)
	}
	if _, err := output(exec.Command("sh", "-c", "echo no such remote >&2; exit 128")); err == nil || !strings.Contains(err.Error(), "no such remote") {
		t.Errorf("a failure's error = %v, want what it said", err)
	}
}
