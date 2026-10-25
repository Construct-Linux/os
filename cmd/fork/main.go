// Command fork moves a Construct-Linux fork a recipe builds from onto its upstream, the part of
// it that needs no judgement:
//
//	fork sync  <recipe> [-ours]   fetch upstream and merge its default branch into the fork's branch: committed when
//	                              clean, left in progress with a report when not (cmd/fork/sync.go)
//	fork check <recipe>           the fork's tree holds: no conflict left, every .js parses, the schemas compile, JSON and
//	                              XML parse, metadata declares GNOME Shell 51 alone, the recipe's patches apply (cmd/fork/check.go)
//	fork pin   <recipe>           the recipe pins the fork's head, at upstream's version or a new epoch (cmd/fork/pin.go)
//	fork list                     every fork the recipes build from: recipe, repository, branch, commit, a line each
//
// A fork is upstream plus the fixes triaged against this stack, one commit each, on the branch
// the recipe names; the checkout is <-forks>/<repository>, beside this repository by default,
// with upstream as its upstream remote. sync writes _output/fork/<repository>/report.{json,md}:
// upstream's commits since the fork's base, the fork's own with the ones upstream now carries
// identically or touches the same files as, and the conflicts. The report is what a person or an
// agent resolves from; check is what says the resolution holds; pin is what moves the image.
// Nothing here pushes: the recipe builds from GitHub, so the branch is pushed by a person first.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Construct-Linux/os/internal/forks"
)

const usage = `usage:
  fork sync  <recipe> [-ours] [-recipes dir] [-forks dir] [-out dir]
  fork check <recipe> [-recipes dir] [-forks dir]
  fork pin   <recipe> [-recipes dir] [-forks dir] [-out dir]
  fork list  [-recipes dir]`

// where is what every command is told: the recipes, the checkouts and the reports.
type where struct {
	recipes, forks, out string
}

func (w *where) flags(fl *flag.FlagSet) {
	fl.StringVar(&w.recipes, "recipes", "recipes", "the recipes directory")
	fl.StringVar(&w.forks, "forks", "..", "the directory holding the forks' checkouts, one per repository")
	fl.StringVar(&w.out, "out", "_output/fork", "where the reports go")
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "sync":
		err = syncCmd(ctx, os.Args[2:], os.Stdout)
	case "check":
		err = checkCmd(ctx, os.Args[2:], os.Stdout)
	case "pin":
		err = pinCmd(ctx, os.Args[2:], os.Stdout)
	case "list":
		err = listCmd(os.Args[2:], os.Stdout)
	default:
		err = fmt.Errorf("unknown command %q\n%s", os.Args[1], usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fork:", err)
		os.Exit(1)
	}
}

// parse reads the flags and the one recipe argument, in either order.
func parse(fl *flag.FlagSet, args []string) (string, error) {
	// Flags after the recipe name too: task fork:sync -- vitals -ours.
	var name string
	var rest []string
	for _, a := range args {
		if name == "" && len(a) > 0 && a[0] != '-' {
			name = a
			continue
		}
		rest = append(rest, a)
	}
	if err := fl.Parse(rest); err != nil {
		return "", err
	}
	if name == "" || fl.NArg() > 0 {
		return "", fmt.Errorf("one recipe name is needed\n%s", usage)
	}
	return name, nil
}

// listCmd prints every fork the recipes build from, for a workflow to loop over.
func listCmd(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("list", flag.ContinueOnError)
	var w where
	w.flags(fl)
	if err := fl.Parse(args); err != nil {
		return err
	}
	pins, err := forks.Read(w.recipes)
	if err != nil {
		return err
	}
	for _, p := range pins {
		fmt.Fprintf(out, "%s %s %s %s\n", strings.TrimSuffix(filepath.Base(p.Recipe), ".yaml"), p.Name, p.Branch, p.Commit)
	}
	return nil
}
