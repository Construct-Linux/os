package program

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// A program that fails says why with the last line it wrote to standard error, and nothing more
// when it wrote none.
func TestRunSaysWhy(t *testing.T) {
	err := Run(context.Background(), "sh", "-c", "echo first >&2; echo FIDO_ERR_ACTION_TIMEOUT >&2; exit 1")
	if err == nil || !strings.HasSuffix(err.Error(), ": FIDO_ERR_ACTION_TIMEOUT") {
		t.Errorf("Run = %v", err)
	}
	err = Run(context.Background(), "false")
	if err == nil || strings.HasSuffix(err.Error(), ": ") {
		t.Errorf("Run = %q", err)
	}
}

// What a program is given reaches it: its input, its environment, and its output goes where asked.
func TestWith(t *testing.T) {
	var out bytes.Buffer
	err := With{Stdin: []byte("in\n"), Env: []string{"WORD=given"}, Stdout: &out}.Run(context.Background(), "sh", "-c", "read x; echo $x $WORD")
	if err != nil || out.String() != "in given\n" {
		t.Errorf("With: %v, %q", err, out.String())
	}
}
