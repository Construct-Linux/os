package main

// The programs the image's commands run, and how: a pipeline into a file, a tree's names, a
// program's status or its output, and a flag that collects.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// pipeline runs cmds in dir, each one's output the next one's input - stdin the first's, dst the
// last's - and fails when any of them does: a shell's pipeline counts only its last program, and a
// cpio that failed half way left a truncated initrd that zstd compressed, exiting 0, into a kernel
// image that was then signed.
func pipeline(dir, dst string, stdin []byte, cmds ...[]string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	procs := make([]*exec.Cmd, len(cmds))
	var in io.Reader = bytes.NewReader(stdin)
	// Once the children hold the pipes, ours would keep a reader waiting for an end that never came.
	var closeAfterStart []*os.File
	closePipes := func() {
		for _, f := range closeAfterStart {
			_ = f.Close()
		}
	}
	for i, c := range cmds {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir, cmd.Stdin, cmd.Stderr = dir, in, os.Stderr
		if i == len(cmds)-1 {
			cmd.Stdout = out
		} else {
			r, w, err := os.Pipe()
			if err != nil {
				closePipes()
				return err
			}
			cmd.Stdout, in = w, r
			closeAfterStart = append(closeAfterStart, r, w)
		}
		procs[i] = cmd
	}
	for i, cmd := range procs {
		if err := cmd.Start(); err != nil {
			// Those already started see their pipes end and stop: waited for, none is left running
			// on a release's files after pipeline has failed.
			closePipes()
			for _, started := range procs[:i] {
				_ = started.Wait()
			}
			return fmt.Errorf("%s: %w", cmd.Args[0], err)
		}
	}
	closePipes()
	var errs []error
	for _, cmd := range procs {
		if err := cmd.Wait(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", cmd.Args[0], err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return out.Close()
}

// treeNames is what `find top | LC_ALL=C sort` lists from dir, a line a path, as cpio reads them:
// top itself and everything under it, in byte order, so the same tree is the same archive.
func treeNames(dir, top string) ([]byte, error) {
	var names []string
	err := filepath.WalkDir(filepath.Join(dir, top), func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if top == "." && rel != "." {
			rel = "./" + rel
		}
		names = append(names, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return []byte(strings.Join(names, "\n") + "\n"), nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// output runs cmd and answers what it printed, trimmed, or its error with what it said on
// stderr: an exit status alone - git's 128, aws's 255 - does not say why.
func output(cmd *exec.Cmd) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args[:min(len(cmd.Args), 2)], " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// appendFlag collects -append's options, one per flag: a value with spaces does not survive the
// quoting task usb passes it through.
type appendFlag []string

func (a *appendFlag) String() string { return strings.Join(*a, " ") }

func (a *appendFlag) Set(v string) error {
	*a = append(*a, v)
	return nil
}
