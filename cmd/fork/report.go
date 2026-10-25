package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A report is what sync found: both sides' commits since the fork's base, how they relate, and
// what the merge did. It is written as JSON for programs and as Markdown for the person or agent
// who resolves the conflicts, and as an issue's body.
type report struct {
	Fork           string `json:"fork"`
	Recipe         string `json:"recipe"`
	Branch         string `json:"branch"`
	Upstream       string `json:"upstream"`        // the remote's URL
	UpstreamBranch string `json:"upstream_branch"` // its default branch
	Base           string `json:"base"`            // the merge base before the merge
	Head           string `json:"head"`            // the fork's head before the merge
	UpstreamHead   string `json:"upstream_head"`
	// Result is at-head (nothing to merge), merged (clean, committed), conflicts (the merge is
	// in progress in the checkout) or recorded (-ours: upstream's head merged, the tree kept).
	Result      string     `json:"result"`
	MergeCommit string     `json:"merge_commit,omitempty"`
	Theirs      []commit   `json:"upstream_commits"` // upstream's since the base, newest first, no merges
	Ours        []commit   `json:"fork_commits"`     // the fork's since the base, newest first, no merges
	Conflicts   []conflict `json:"conflicts,omitempty"`
	Merged      []string   `json:"merged_files,omitempty"` // files the merge changed without conflict
	// Untouched are the paths upstream changed since the base that the fork never did, and how
	// many of them differ between upstream's head and the fork's. Many upstream commits, no
	// difference on them and conflicts everywhere else is a squash: upstream's branch is the
	// fork's own history released as one commit, and the merge records it with -ours.
	Untouched struct {
		Paths  int `json:"paths"`
		Differ int `json:"differ"`
	} `json:"untouched"`
}

type commit struct {
	SHA     string   `json:"sha"`
	Date    string   `json:"date"`
	Subject string   `json:"subject"`
	Files   []string `json:"files"`
	// Identical is the other side's commit with the same patch id: the fork's fix upstream
	// merged as it was, or upstream's fix the fork took. It needs no merge.
	Identical string `json:"identical,omitempty"`
	// Overlaps are the other side's commits touching a file this one touches: where the two
	// fixes of one thing, or a feature over a fix, will meet.
	Overlaps []string `json:"overlaps,omitempty"`
}

type conflict struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"` // both modified, deleted by the fork, deleted upstream, both added
	Hunks int    `json:"hunks,omitempty"`
}

// write puts the report in dir as report.json and report.md.
func (r *report) write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(r.markdown()), 0o644)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// markdown is the report as a person or an agent reads it: what each side did, where they meet,
// what the merge left, and what to do next.
func (r *report) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", r.Fork, r.Result)
	fmt.Fprintf(&b, "%s builds Construct-Linux/%s, branch %s, from `%s`. Upstream is %s, branch %s, at `%s`; the common base is `%s`.\n\n",
		r.Recipe, r.Fork, r.Branch, short(r.Head), r.Upstream, r.UpstreamBranch, short(r.UpstreamHead), short(r.Base))
	switch r.Result {
	case "at-head":
		b.WriteString("The fork carries everything upstream's branch has: nothing to merge.\n")
		return b.String()
	case "merged":
		fmt.Fprintf(&b, "Upstream merged cleanly, committed as `%s`. Run `fork check`, read the files the merge changed, then `fork pin`.\n\n", short(r.MergeCommit))
	case "recorded":
		fmt.Fprintf(&b, "Upstream's head is recorded as merged, the fork's tree kept, committed as `%s`.\n\n", short(r.MergeCommit))
	case "conflicts":
		fmt.Fprintf(&b, "The merge is in progress in the checkout with %d file(s) in conflict. Resolve them, `git add` each, "+
			"run `fork check`, commit the merge, then `fork pin`. `git merge --abort` undoes it.\n\n", len(r.Conflicts))
	}
	if len(r.Conflicts) > 0 {
		b.WriteString("## Conflicts\n\n| file | kind | hunks |\n|---|---|---:|\n")
		for _, c := range r.Conflicts {
			fmt.Fprintf(&b, "| %s | %s | %d |\n", c.Path, c.Kind, c.Hunks)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## Upstream's %d commit(s) since the base\n\n| date | commit | subject | files | meets |\n|---|---|---|---|---|\n", len(r.Theirs))
	for _, c := range r.Theirs {
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s | %s |\n", c.Date, c.SHA[:7], escape(c.Subject), escape(strings.Join(c.Files, ", ")), meets(c))
	}
	fmt.Fprintf(&b, "\n## The fork's %d commit(s) since the base\n\n| date | commit | subject | files | meets |\n|---|---|---|---|---|\n", len(r.Ours))
	for _, c := range r.Ours {
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s | %s |\n", c.Date, c.SHA[:7], escape(c.Subject), escape(strings.Join(c.Files, ", ")), meets(c))
	}
	fmt.Fprintf(&b, "\n## Where they meet\n\nUpstream changed %d path(s) the fork never touched since the base; upstream's head and the fork's differ on %d of them.",
		r.Untouched.Paths, r.Untouched.Differ)
	if r.Untouched.Paths > 0 && r.Untouched.Differ == 0 && len(r.Conflicts) > 0 {
		b.WriteString(" Upstream's commits add nothing the fork lacks on those paths: likely upstream released the branch the fork was cut from, squashed. " +
			"Check the delta on the fork's own paths; when it is already here, `git merge --abort` and run `fork sync -ours`.")
	}
	b.WriteString("\n")
	if len(r.Merged) > 0 {
		fmt.Fprintf(&b, "\n## Files the merge changed without conflict\n\n%s\n", strings.Join(r.Merged, ", "))
	}
	return b.String()
}

func meets(c commit) string {
	var parts []string
	if c.Identical != "" {
		parts = append(parts, "identical to `"+c.Identical[:7]+"`")
	}
	for _, o := range c.Overlaps {
		parts = append(parts, "`"+o[:7]+"`")
	}
	return strings.Join(parts, ", ")
}

func escape(s string) string { return strings.ReplaceAll(s, "|", "\\|") }
