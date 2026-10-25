package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// A fork is a Construct-Linux repository a recipe builds from: upstream plus the fixes triaged
// against this stack, one commit each, pinned at a commit. What upstream merges after the fork's
// base - a fix the fork made its own way, a feature, a release - reaches the desktop only when a
// person rebases the branch and moves the pin, so a fork behind its upstream is an issue, as a pin
// behind its release is.
type fork struct {
	recipe string // the recipe's path, as given
	name   string // the repository under Construct-Linux
	branch string
	commit string // expected-commit, the fork's head the recipe builds
}

// forkCheckout is a recipe's git-checkout of a Construct-Linux repository: melange's three lines,
// in the order the recipes write them.
var forkCheckout = regexp.MustCompile(`(?m)^[ \t]+repository: https://github\.com/Construct-Linux/([^\s/]+?)(?:\.git)?[ \t]*\n[ \t]+branch: (\S+)[ \t]*\n[ \t]+expected-commit: ([0-9a-f]{40})[ \t]*$`)

// readForks is every fork the recipes build from, in the recipes' order.
func readForks(recipes string) ([]fork, error) {
	paths, err := filepath.Glob(filepath.Join(recipes, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var forks []fork
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		for _, m := range forkCheckout.FindAllStringSubmatch(string(b), -1) {
			forks = append(forks, fork{recipe: p, name: m[1], branch: m[2], commit: m[3]})
		}
	}
	return forks, nil
}

// compared is what GitHub says of the fork's pinned commit against upstream's default branch.
type compared struct {
	URL     string `json:"html_url"`
	AheadBy int    `json:"ahead_by"`  // upstream's commits the pin lacks
	Behind  int    `json:"behind_by"` // the fork's own commits, which upstream lacks
	Commits []struct {
		SHA     string `json:"sha"`
		URL     string `json:"html_url"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
		Commit struct {
			Message   string `json:"message"`
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	} `json:"commits"`
}

// listed is how many of upstream's commits an issue lists: enough to read a release's worth, not a
// year's.
const listed = 50

// forksCheck compares each fork a recipe pins with its upstream: one issue per fork whose pinned
// commit lacks commits of upstream's default branch, listing them, closed once the fork is rebased
// and the recipe pins the new head.
func (s sources) forksCheck(recipes string) check {
	return check{
		scope: "forks",
		gone:  "the recipe pins a commit on upstream's head: closing.",
		find: func(ctx context.Context) ([]finding, error) {
			forks, err := readForks(recipes)
			if err != nil {
				return nil, err
			}
			var found []finding
			var errs []error
			for _, f := range forks {
				fd, err := s.forkFinding(ctx, f)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", f.name, err))
					continue
				}
				if fd != nil {
					found = append(found, *fd)
				}
			}
			// One upstream that did not answer leaves the issues as they are, rather than closing
			// one whose fork is still behind.
			if err := errors.Join(errs...); err != nil {
				return nil, err
			}
			return found, nil
		},
	}
}

// forkFinding is f's issue, or nil when its pin lacks nothing of upstream's. A Construct-Linux
// repository GitHub knows no parent of (the brand) is the project's own, with no upstream to lag.
func (s sources) forkFinding(ctx context.Context, f fork) (*finding, error) {
	var repo struct {
		Parent *struct {
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
		} `json:"parent"`
	}
	ours := s.github + "/repos/Construct-Linux/" + f.name
	if err := s.getJSON(ctx, ours, &repo); err != nil {
		return nil, err
	}
	if repo.Parent == nil {
		return nil, nil
	}
	owner, _, _ := strings.Cut(repo.Parent.FullName, "/")
	// Without a page parameter the compare lists up to 250 commits; ahead_by counts them all.
	var c compared
	if err := s.getJSON(ctx, ours+"/compare/"+f.commit+"..."+owner+":"+repo.Parent.DefaultBranch, &c); err != nil {
		return nil, err
	}
	if c.AheadBy == 0 {
		return nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s builds Construct-Linux/%s at `%s` (branch %s), %d commit(s) of its own over its base. "+
		"%s's %s has %d commit(s) the fork lacks ([compare](%s)):\n\n| date | commit | subject |\n|---|---|---|\n",
		f.recipe, f.name, f.commit[:12], f.branch, c.Behind, repo.Parent.FullName, repo.Parent.DefaultBranch, c.AheadBy, c.URL)
	// Newest first, as git log reads; a merge commit says nothing its commits do not.
	rows := 0
	for i := len(c.Commits) - 1; i >= 0; i-- {
		cm := c.Commits[i]
		if len(cm.Parents) > 1 {
			continue
		}
		if rows == listed {
			fmt.Fprintf(&b, "| | | ... and the rest, at the compare |\n")
			break
		}
		subject, _, _ := strings.Cut(cm.Commit.Message, "\n")
		fmt.Fprintf(&b, "| %s | [`%s`](%s) | %s |\n", cm.Commit.Committer.Date.Format("2006-01-02"), cm.SHA[:7], cm.URL, strings.ReplaceAll(strings.TrimSpace(subject), "|", "\\|"))
		rows++
	}
	fmt.Fprintf(&b, "\nRead them against the fork's own commits: a fix the fork made its own way is dropped for upstream's, a feature is taken "+
		"or reverted on the branch with why, and a release is what the rest hang on. Then rebase %s onto %s, pin the new head in %s, "+
		"and run `task test` for the shell's startup and idle against the last build's.\n", f.branch, repo.Parent.DefaultBranch, f.recipe)
	return &finding{key: "forks:" + f.name, title: fmt.Sprintf("update: %s is %d commit(s) behind %s", f.name, c.AheadBy, repo.Parent.FullName),
		labels: []string{"update", "area:desktop"}, body: b.String()}, nil
}
