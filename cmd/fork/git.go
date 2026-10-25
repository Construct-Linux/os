package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// repo is a fork's checkout, spoken to through git.
type repo struct {
	dir string
}

// git runs git in the checkout and gives its output, trimmed; a failure says what git said.
func (r repo) git(ctx context.Context, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = r.dir
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return strings.TrimSpace(string(out)), fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// lines is git's output as lines, none when it said nothing.
func (r repo) lines(ctx context.Context, args ...string) ([]string, error) {
	out, err := r.git(ctx, args...)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// onBranch checks the checkout is on branch with nothing uncommitted: sync and pin work on the
// branch the recipe builds, and a dirty tree would be merged or pinned by accident.
func (r repo) onBranch(ctx context.Context, branch string) error {
	cur, err := r.git(ctx, "branch", "--show-current")
	if err != nil {
		return err
	}
	if cur != branch {
		return fmt.Errorf("%s is on %q, not on the recipe's branch %s", r.dir, cur, branch)
	}
	status, err := r.git(ctx, "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("%s has uncommitted changes:\n%s", r.dir, status)
	}
	return nil
}

// upstreamBranch is the default branch of the upstream remote, asked of the remote itself: the
// local refs/remotes/upstream/HEAD is set only by clone, and these checkouts added upstream later.
func (r repo) upstreamBranch(ctx context.Context) (string, error) {
	if _, err := r.git(ctx, "remote", "get-url", "upstream"); err != nil {
		return "", errors.New("the checkout has no upstream remote: git remote add upstream <url>")
	}
	out, err := r.git(ctx, "ls-remote", "--symref", "upstream", "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if ref, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			return strings.Fields(ref)[0], nil
		}
	}
	return "", fmt.Errorf("upstream's HEAD names no branch:\n%s", out)
}

// patchIDs maps each commit in rng (a rev-list range) to its stable patch id, and back: two
// commits of one change made on both sides have the same id whatever their hashes.
func (r repo) patchIDs(ctx context.Context, rng string) (map[string]string, error) {
	shas, err := r.lines(ctx, "rev-list", "--no-merges", rng)
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, sha := range shas {
		show := exec.CommandContext(ctx, "git", "show", "--format=", sha)
		show.Dir = r.dir
		diff, err := show.Output()
		if err != nil {
			return nil, fmt.Errorf("git show %s: %w", sha, err)
		}
		id := exec.CommandContext(ctx, "git", "patch-id", "--stable")
		id.Dir, id.Stdin = r.dir, bytes.NewReader(diff)
		out, err := id.Output()
		if err != nil {
			return nil, fmt.Errorf("git patch-id %s: %w", sha, err)
		}
		if f := strings.Fields(string(out)); len(f) > 0 {
			ids[sha] = f[0]
		}
	}
	return ids, nil
}
