package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// The release tooling's AWS calls go through the CLI, with the credentials and region a person's
// or the builder's environment gives it: the cache (cache.go) and the builder's log (releaselog.go).

// aws runs the AWS CLI with its output on the terminal: s3 sync and cp's progress.
func aws(args ...string) error {
	cmd := exec.Command("aws", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// awsOut runs the AWS CLI and answers what it printed, or its error with what it said.
func awsOut(args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.Command("aws", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("aws %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}
