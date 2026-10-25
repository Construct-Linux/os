package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Construct-Linux/os/internal/apkver"
	"github.com/Construct-Linux/os/internal/forks"
)

// pinCmd moves the recipe to the fork's head once it carries upstream's: the version becomes
// upstream's when that is newer than the recipe's (an extension's metadata.json, or the newest
// tag on upstream's branch), the epoch moves up otherwise, so the builder makes a new apk. The
// recipe's comment above the checkout is a person's: pin says to update it.
func pinCmd(ctx context.Context, args []string, out io.Writer) error {
	fl := flag.NewFlagSet("pin", flag.ContinueOnError)
	var w where
	w.flags(fl)
	name, err := parse(fl, args)
	if err != nil {
		return err
	}
	pin, err := forks.Find(w.recipes, name)
	if err != nil {
		return err
	}
	r := repo{filepath.Join(w.forks, pin.Name)}
	if err := r.onBranch(ctx, pin.Branch); err != nil {
		return err
	}
	head, err := r.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head == pin.Commit {
		return fmt.Errorf("%s already pins %s", pin.Recipe, short(head))
	}
	branch, err := upstreamBranchOf(ctx, r, filepath.Join(w.out, pin.Name))
	if err != nil {
		return err
	}
	if _, err := r.git(ctx, "merge-base", "--is-ancestor", "upstream/"+branch, "HEAD"); err != nil {
		return fmt.Errorf("%s does not carry upstream/%s: fork sync first", pin.Branch, branch)
	}
	ver, from := upstreamVersion(ctx, r, branch)
	version, epoch := pin.Version, pin.Epoch+1
	if ver != "" && apkver.Compare(ver, pin.Version) > 0 {
		version, epoch = ver, 0
	}
	if err := pin.Move(head, version, epoch); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %s -> %s, version %s epoch %d -> %s epoch %d", pin.Recipe, short(pin.Commit), short(head), pin.Version, pin.Epoch, version, epoch)
	if ver != "" {
		fmt.Fprintf(out, " (upstream's %s from %s)", ver, from)
	}
	fmt.Fprintln(out)
	if on, _ := r.git(ctx, "branch", "-r", "--contains", head); on == "" {
		fmt.Fprintf(out, "note: %s is on no remote branch yet: push %s before task build, which clones it from GitHub\n", short(head), pin.Branch)
	}
	fmt.Fprintf(out, "next: say in the comment above git-checkout in %s what this merge brings, then task build -- %s\n",
		pin.Recipe, strings.TrimSuffix(filepath.Base(pin.Recipe), ".yaml"))
	return nil
}

// upstreamBranchOf is upstream's default branch: the report's when sync wrote one, else asked
// of the remote.
func upstreamBranchOf(ctx context.Context, r repo, dir string) (string, error) {
	if b, err := os.ReadFile(filepath.Join(dir, "report.json")); err == nil {
		var rep report
		if json.Unmarshal(b, &rep) == nil && rep.UpstreamBranch != "" {
			return rep.UpstreamBranch, nil
		}
	}
	return r.upstreamBranch(ctx)
}

// upstreamVersion is the version upstream's head calls itself: an extension's metadata.json
// version-name or version, else the newest tag reachable from the branch, without its v. Empty
// when neither says.
func upstreamVersion(ctx context.Context, r repo, branch string) (string, string) {
	files, _ := r.lines(ctx, "ls-tree", "-r", "--name-only", "upstream/"+branch)
	for _, f := range files {
		if filepath.Base(f) != "metadata.json" || strings.Contains(f, "node_modules/") {
			continue
		}
		b, err := r.git(ctx, "show", "upstream/"+branch+":"+f)
		if err != nil {
			continue
		}
		var m struct {
			Version     json.Number `json:"version"`
			VersionName string      `json:"version-name"`
		}
		if json.Unmarshal([]byte(b), &m) != nil {
			continue
		}
		if m.VersionName != "" {
			return strings.TrimPrefix(m.VersionName, "v"), f
		}
		if m.Version != "" {
			return m.Version.String(), f
		}
	}
	if tag, err := r.git(ctx, "describe", "--tags", "--abbrev=0", "upstream/"+branch); err == nil && tag != "" {
		return strings.TrimPrefix(tag, "v"), "tag " + tag
	}
	return "", ""
}
