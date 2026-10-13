package main

import (
	"os/exec"
	"strings"
	"testing"
)

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
