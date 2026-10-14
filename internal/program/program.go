// Package program runs the programs the desktop's commands lean on - cryptsetup, systemd's, mkfs,
// the FIDO2 tools - and says what failed: the program, its arguments and the last line it wrote
// to standard error, never what it was given in its environment, which holds the password.
package program

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Run runs name with args, its output on construct's standard error.
func Run(ctx context.Context, name string, args ...string) error {
	return With{}.Run(ctx, name, args...)
}

// With is what a program is given besides its arguments: its standard input, variables added to
// construct's environment, and where its standard output goes, construct's standard error without.
type With struct {
	Stdin  []byte
	Env    []string
	Stdout io.Writer
}

// Run runs name with args as w says.
func (w With) Run(ctx context.Context, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	if w.Stdin != nil {
		c.Stdin = bytes.NewReader(w.Stdin)
	}
	c.Env = append(os.Environ(), w.Env...)
	var said bytes.Buffer
	c.Stdout, c.Stderr = os.Stderr, io.MultiWriter(os.Stderr, &said)
	if w.Stdout != nil {
		c.Stdout = w.Stdout
	}
	if err := c.Run(); err != nil {
		err = fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		if said := strings.TrimSpace(said.String()); said != "" {
			err = fmt.Errorf("%w: %s", err, said[strings.LastIndex(said, "\n")+1:])
		}
		return err
	}
	return nil
}
