// Command watch keeps the repository's GitHub side in order, from a workflow or a person's shell:
//
//	watch labels [-dry-run]                               create or update the issue labels .github/labels.json lists
//	watch run [-grype FILE [-release TAG]] [-dry-run]     open, update and close the issues of security fixes and big updates
//
// run opens an issue only for what a person must act on: a Critical or High vulnerability a
// version fixes in the latest release (Grype's report of its SBOM), a new kernel series or the
// pinned one's end, a new GNOME major, and Intel's microcode, whose releases are security fixes.
// Each issue carries a hidden marker, so the next run updates it, and closes it once its finding
// is gone.
//
// It talks to the repository GITHUB_REPOSITORY names with GITHUB_TOKEN, the two a workflow's job
// has; -dry-run reads the repository when they are set and writes nothing.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const usage = `usage:
  watch labels [-dry-run]
  watch run    [-grype FILE] [-release TAG] [-dry-run]`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "labels":
		err = labelsCmd(ctx, os.Args[2:], os.Stdout)
	case "run":
		err = runCmd(ctx, os.Args[2:], os.Stdout)
	default:
		err = fmt.Errorf("unknown command %q\n%s", os.Args[1], usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "watch:", err)
		os.Exit(1)
	}
}
