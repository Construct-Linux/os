package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/forks"
)

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

// A fork a recipe builds from is upstream plus the fixes triaged against this stack, pinned at a
// commit; what upstream merges after the fork's base reaches the desktop only when a person
// merges upstream and moves the pin (cmd/fork), so a fork behind its upstream is an issue, as a
// pin behind its release is.
//
// forksCheck compares each fork a recipe pins with its upstream: one issue per fork whose pinned
// commit lacks commits of upstream's default branch, listing them, closed once the fork is rebased
// and the recipe pins the new head.
func (s sources) forksCheck(recipes string) check {
	return check{
		scope: "forks",
		gone:  "the recipe pins a commit on upstream's head: closing.",
		find: func(ctx context.Context) ([]finding, error) {
			pins, err := forks.Read(recipes)
			if err != nil {
				return nil, err
			}
			var found []finding
			var errs []error
			for _, f := range pins {
				fd, err := s.forkFinding(ctx, f)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", f.Name, err))
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
func (s sources) forkFinding(ctx context.Context, f forks.Pin) (*finding, error) {
	var repo struct {
		Parent *struct {
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
		} `json:"parent"`
	}
	ours := s.github + "/repos/Construct-Linux/" + f.Name
	if err := s.getJSON(ctx, ours, &repo); err != nil {
		return nil, err
	}
	if repo.Parent == nil {
		return nil, nil
	}
	owner, _, _ := strings.Cut(repo.Parent.FullName, "/")
	// Without a page parameter the compare lists up to 250 commits; ahead_by counts them all.
	var c compared
	if err := s.getJSON(ctx, ours+"/compare/"+f.Commit+"..."+owner+":"+repo.Parent.DefaultBranch, &c); err != nil {
		return nil, err
	}
	if c.AheadBy == 0 {
		return nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s builds Construct-Linux/%s at `%s` (branch %s), %d commit(s) of its own over its base. "+
		"%s's %s has %d commit(s) the fork lacks ([compare](%s)):\n\n| date | commit | subject |\n|---|---|---|\n",
		f.Recipe, f.Name, f.Commit[:12], f.Branch, c.Behind, repo.Parent.FullName, repo.Parent.DefaultBranch, c.AheadBy, c.URL)
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
	fmt.Fprintf(&b, "\n`task fork:sync -- %s` merges %s into %s and writes the report; the fork-merge skill resolves what conflicts; "+
		"`task fork:check` and `task fork:pin -- %s` verify the tree and move this pin.\n", f.Recipe, repo.Parent.DefaultBranch, f.Branch, f.Recipe)
	return &finding{key: "forks:" + f.Name, title: fmt.Sprintf("update: %s is %d commit(s) behind %s", f.Name, c.AheadBy, repo.Parent.FullName),
		labels: []string{"update", "area:desktop"}, body: b.String()}, nil
}
