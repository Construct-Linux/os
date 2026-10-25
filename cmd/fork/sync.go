package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Construct-Linux/os/internal/forks"
)

// syncCmd fetches upstream and merges its default branch into the fork's branch. Clean, the
// merge is committed with upstream's subjects as its message; not, it is left in progress with
// the report saying where the two sides meet. -ours records upstream's head as merged and keeps
// the fork's tree, for an upstream whose branch is the fork's own history squashed.
func syncCmd(ctx context.Context, args []string, out io.Writer) error {
	fl := flag.NewFlagSet("sync", flag.ContinueOnError)
	var w where
	w.flags(fl)
	ours := fl.Bool("ours", false, "merge upstream's head keeping the fork's tree, when the report shows upstream released what the fork already has")
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
	if _, err := r.git(ctx, "fetch", "-q", "--tags", "upstream"); err != nil {
		return err
	}
	rep, err := analyse(ctx, r, pin)
	if err != nil {
		return err
	}
	dir := filepath.Join(w.out, pin.Name)
	up := "upstream/" + rep.UpstreamBranch
	switch {
	case rep.Head == rep.UpstreamHead || rep.Base == rep.UpstreamHead:
		rep.Result = "at-head"
	case *ours:
		msg := fmt.Sprintf("Merge upstream %s into %s, keeping the fork's tree\n\nUpstream released what the fork already carries commit by commit; nothing to take.\n", rep.UpstreamBranch, pin.Branch)
		if _, err := r.git(ctx, "merge", "-s", "ours", "--no-ff", "-m", msg, up); err != nil {
			return err
		}
		rep.Result = "recorded"
		rep.MergeCommit, _ = r.git(ctx, "rev-parse", "HEAD")
	default:
		_, mergeErr := r.git(ctx, "merge", "--no-ff", "--no-commit", up)
		unmerged, err := r.lines(ctx, "diff", "--name-only", "--diff-filter=U")
		if err != nil {
			return err
		}
		if mergeErr != nil && len(unmerged) == 0 {
			return mergeErr
		}
		rep.Merged, _ = r.lines(ctx, "diff", "--cached", "--name-only", "--diff-filter=AMDR")
		if len(unmerged) > 0 {
			rep.Result = "conflicts"
			rep.Conflicts, err = conflicts(ctx, r, unmerged)
			if err != nil {
				return err
			}
		} else {
			rep.Result = "merged"
			if _, err := r.git(ctx, "commit", "-q", "-m", rep.mergeMessage(pin.Branch)); err != nil {
				return err
			}
			rep.MergeCommit, _ = r.git(ctx, "rev-parse", "HEAD")
		}
	}
	if err := rep.write(dir); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %s", pin.Name, rep.Result)
	switch rep.Result {
	case "conflicts":
		fmt.Fprintf(out, " in %d file(s)", len(rep.Conflicts))
	case "merged", "recorded":
		fmt.Fprintf(out, " as %s", short(rep.MergeCommit))
	}
	fmt.Fprintf(out, "; %s\n", filepath.Join(dir, "report.md"))
	return nil
}

// analyse is the report before the merge: both sides' commits since the base and how they meet.
func analyse(ctx context.Context, r repo, pin forks.Pin) (*report, error) {
	rep := &report{Fork: pin.Name, Recipe: pin.Recipe, Branch: pin.Branch}
	var err error
	if rep.Upstream, err = r.git(ctx, "remote", "get-url", "upstream"); err != nil {
		return nil, err
	}
	if rep.UpstreamBranch, err = r.upstreamBranch(ctx); err != nil {
		return nil, err
	}
	up := "upstream/" + rep.UpstreamBranch
	if rep.Head, err = r.git(ctx, "rev-parse", "HEAD"); err != nil {
		return nil, err
	}
	if rep.UpstreamHead, err = r.git(ctx, "rev-parse", up); err != nil {
		return nil, err
	}
	if rep.Base, err = r.git(ctx, "merge-base", "HEAD", up); err != nil {
		return nil, err
	}
	if rep.Theirs, err = commits(ctx, r, "HEAD.."+up); err != nil {
		return nil, err
	}
	if rep.Ours, err = commits(ctx, r, up+"..HEAD"); err != nil {
		return nil, err
	}
	theirIDs, err := r.patchIDs(ctx, "HEAD.."+up)
	if err != nil {
		return nil, err
	}
	ourIDs, err := r.patchIDs(ctx, up+"..HEAD")
	if err != nil {
		return nil, err
	}
	byID := map[string]string{}
	for sha, id := range theirIDs {
		byID[id] = sha
	}
	for i := range rep.Ours {
		if sha, ok := byID[ourIDs[rep.Ours[i].SHA]]; ok {
			rep.Ours[i].Identical = sha
		}
	}
	byID = map[string]string{}
	for sha, id := range ourIDs {
		byID[id] = sha
	}
	for i := range rep.Theirs {
		if sha, ok := byID[theirIDs[rep.Theirs[i].SHA]]; ok {
			rep.Theirs[i].Identical = sha
		}
	}
	// Overlaps: the other side's commits touching one of this commit's files.
	touched := func(cs []commit) map[string][]string {
		m := map[string][]string{}
		for _, c := range cs {
			for _, f := range c.Files {
				m[f] = append(m[f], c.SHA)
			}
		}
		return m
	}
	ourFiles, theirFiles := touched(rep.Ours), touched(rep.Theirs)
	// An identical commit meets its twin by nature; its overlaps would say nothing.
	for i := range rep.Ours {
		if rep.Ours[i].Identical == "" {
			rep.Ours[i].Overlaps = overlapping(rep.Ours[i], theirFiles)
		}
	}
	for i := range rep.Theirs {
		if rep.Theirs[i].Identical == "" {
			rep.Theirs[i].Overlaps = overlapping(rep.Theirs[i], ourFiles)
		}
	}
	// The paths upstream changed and the fork never did, and whether the heads differ there.
	var untouched []string
	for f := range theirFiles {
		if _, ok := ourFiles[f]; !ok {
			untouched = append(untouched, f)
		}
	}
	sort.Strings(untouched)
	rep.Untouched.Paths = len(untouched)
	if len(untouched) > 0 {
		differ, err := r.lines(ctx, append([]string{"diff", "--name-only", "HEAD", up, "--"}, untouched...)...)
		if err != nil {
			return nil, err
		}
		rep.Untouched.Differ = len(differ)
	}
	return rep, nil
}

func overlapping(c commit, other map[string][]string) []string {
	seen := map[string]bool{}
	var shas []string
	for _, f := range c.Files {
		for _, sha := range other[f] {
			if !seen[sha] {
				seen[sha] = true
				shas = append(shas, sha)
			}
		}
	}
	sort.Strings(shas)
	return shas
}

// commits are rng's, newest first, merges left out, each with the files it touches.
func commits(ctx context.Context, r repo, rng string) ([]commit, error) {
	// A NUL ends each commit's record; the files follow the header on their own lines.
	out, err := r.git(ctx, "log", "--no-merges", "--format=%x00%H%x1f%cs%x1f%s", "--name-only", rng)
	if err != nil {
		return nil, err
	}
	var cs []commit
	for _, rec := range strings.Split(out, "\x00") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		header, files, _ := strings.Cut(rec, "\n")
		h := strings.Split(header, "\x1f")
		if len(h) != 3 {
			continue
		}
		c := commit{SHA: h[0], Date: h[1], Subject: h[2]}
		for _, f := range strings.Split(files, "\n") {
			if f = strings.TrimSpace(f); f != "" {
				c.Files = append(c.Files, f)
			}
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// conflicts names each unmerged file's kind and counts its conflict hunks.
func conflicts(ctx context.Context, r repo, paths []string) ([]conflict, error) {
	status, err := r.lines(ctx, append([]string{"status", "--porcelain", "--"}, paths...)...)
	if err != nil {
		return nil, err
	}
	kinds := map[string]string{}
	for _, line := range status {
		if len(line) < 4 {
			continue
		}
		kind := "both modified"
		switch line[:2] {
		case "DU":
			kind = "deleted by the fork, changed upstream"
		case "UD":
			kind = "changed by the fork, deleted upstream"
		case "AA":
			kind = "added by both"
		case "AU", "UA":
			kind = "added by one side"
		}
		kinds[line[3:]] = kind
	}
	var cs []conflict
	for _, p := range paths {
		c := conflict{Path: p, Kind: kinds[p]}
		if c.Kind == "" {
			c.Kind = "both modified"
		}
		if b, err := os.ReadFile(filepath.Join(r.dir, p)); err == nil {
			c.Hunks = strings.Count("\n"+string(b), "\n<<<<<<< ")
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// mergeMessage is a clean merge's commit message: upstream's subjects, newest first.
func (r *report) mergeMessage(branch string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Merge upstream %s into %s\n\nUpstream's commits since the fork's base, newest first:\n\n", r.UpstreamBranch, branch)
	for _, c := range r.Theirs {
		fmt.Fprintf(&b, "- %s %s\n", c.SHA[:7], c.Subject)
	}
	return b.String()
}
