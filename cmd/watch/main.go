// Command watch keeps the repository's GitHub side in order, from a workflow or a person's shell:
//
//	watch labels [-file .github/labels.json] [-dry-run]   create or update the issue labels the file lists
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
  watch labels [-file FILE] [-dry-run]`

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
	default:
		err = fmt.Errorf("unknown command %q\n%s", os.Args[1], usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "watch:", err)
		os.Exit(1)
	}
}
