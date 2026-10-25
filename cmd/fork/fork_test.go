package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// git runs git in dir for a test, failing it on an error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A world is an upstream repository, the fork cloned from it on gnome-51 with upstream as a
// remote, a recipe pinning the fork's head, and where the reports go.
type world struct {
	upstream, fork, forks, recipes, out string
}

func (w world) args(name string) []string {
	return []string{name, "-recipes", w.recipes, "-forks", w.forks, "-out", w.out}
}

func setup(t *testing.T) world {
	t.Helper()
	root := t.TempDir()
	// No signing, hooks or identity from the person's own configuration.
	cfg := filepath.Join(root, "gitconfig")
	write(t, root, "gitconfig", "[user]\n\tname = test\n\temail = test@example.com\n[init]\n\tdefaultBranch = main\n")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	w := world{upstream: filepath.Join(root, "upstream"), forks: filepath.Join(root, "forks"), recipes: filepath.Join(root, "recipes"), out: filepath.Join(root, "out")}
	w.fork = filepath.Join(w.forks, "thefork")
	for _, d := range []string{w.upstream, w.forks, w.recipes} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, w.upstream, "init", "-q")
	write(t, w.upstream, "a.txt", "one\ntwo\nthree\n")
	write(t, w.upstream, "metadata.json", "{\n  \"version\": 1,\n  \"uuid\": \"thefork@example.com\",\n  \"name\": \"The fork\",\n  \"shell-version\": [\"49\", \"50\", \"51\"]\n}\n")
	write(t, w.upstream, "ci.yml", "upstream's CI\n")
	git(t, w.upstream, "add", "-A")
	git(t, w.upstream, "commit", "-q", "-m", "base")
	git(t, w.forks, "clone", "-q", w.upstream, "thefork")
	git(t, w.fork, "remote", "rename", "origin", "upstream")
	git(t, w.fork, "checkout", "-q", "-b", "gnome-51")
	// The fork: 51 alone, upstream's CI gone, and one fix of its own on a.txt's first line.
	write(t, w.fork, "metadata.json", "{\n  \"version\": 1,\n  \"uuid\": \"thefork@example.com\",\n  \"name\": \"The fork\",\n  \"shell-version\": [\"51\"]\n}\n")
	git(t, w.fork, "rm", "-q", "ci.yml")
	git(t, w.fork, "commit", "-q", "-a", "-m", "metadata: declare GNOME Shell 51 only, drop upstream's CI")
	write(t, w.fork, "a.txt", "one, the fork's way\ntwo\nthree\n")
	git(t, w.fork, "commit", "-q", "-a", "-m", "a: fix the first line the fork's way")
	write(t, w.recipes, "thefork.yaml", "package:\n  name: thefork\n  version: \"1\"\n  epoch: 2\n\npipeline:\n  - uses: git-checkout\n    with:\n      repository: https://github.com/Construct-Linux/thefork\n      branch: gnome-51\n      expected-commit: "+git(t, w.fork, "rev-parse", "HEAD")+"\n")
	return w
}

func (w world) report(t *testing.T) report {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(w.out, "thefork", "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// Upstream fixed the same line another way and added a file; the fork took one upstream commit
// as it was. sync stops on the conflict and says where the sides meet; check refuses the tree
// until it is resolved; the commit and pin follow.
func TestConflictThenResolve(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	write(t, w.upstream, "a.txt", "one, upstream's way\ntwo\nthree\n")
	git(t, w.upstream, "commit", "-q", "-a", "-m", "a: fix the first line")
	write(t, w.upstream, "b.txt", "b\n")
	git(t, w.upstream, "add", "b.txt")
	git(t, w.upstream, "commit", "-q", "-m", "b: add")
	write(t, w.upstream, "ci.yml", "upstream's CI, changed\n")
	git(t, w.upstream, "commit", "-q", "-a", "-m", "ci: change")
	taken := git(t, w.upstream, "rev-parse", "HEAD~1")
	git(t, w.fork, "fetch", "-q", "upstream")
	git(t, w.fork, "cherry-pick", taken)

	var out bytes.Buffer
	if err := syncCmd(ctx, w.args("thefork"), &out); err != nil {
		t.Fatal(err)
	}
	r := w.report(t)
	if r.Result != "conflicts" || !strings.Contains(out.String(), "conflicts in 2 file(s)") {
		t.Fatalf("%s\n%+v", out.String(), r)
	}
	kinds := map[string]string{}
	for _, c := range r.Conflicts {
		kinds[c.Path] = c.Kind
	}
	if kinds["a.txt"] != "both modified" || kinds["ci.yml"] != "deleted by the fork, changed upstream" {
		t.Errorf("conflicts: %+v", r.Conflicts)
	}
	if len(r.Theirs) != 3 || len(r.Ours) != 3 {
		t.Fatalf("commits: %d upstream, %d ours", len(r.Theirs), len(r.Ours))
	}
	var identical, overlaps int
	for _, c := range append(r.Theirs, r.Ours...) {
		if c.Identical != "" {
			identical++
		}
		if len(c.Overlaps) > 0 {
			overlaps++
		}
	}
	if identical != 2 || overlaps != 4 {
		t.Errorf("%d identical and %d overlapping, want b's two and a's and ci's four:\n%+v\n%+v", identical, overlaps, r.Theirs, r.Ours)
	}
	md, _ := os.ReadFile(filepath.Join(w.out, "thefork", "report.md"))
	if !strings.Contains(string(md), "| a.txt | both modified | 1 |") || !strings.Contains(string(md), "identical to") {
		t.Errorf("report.md:\n%s", md)
	}

	out.Reset()
	if err := checkCmd(ctx, w.args("thefork"), &out); err == nil || !strings.Contains(out.String(), "unresolved") {
		t.Errorf("check on a conflict: %v\n%s", err, out.String())
	}
	// Resolved: upstream's line, the CI stays gone, the merge committed.
	write(t, w.fork, "a.txt", "one, upstream's way\ntwo\nthree\n")
	git(t, w.fork, "add", "a.txt")
	git(t, w.fork, "rm", "-q", "ci.yml")
	out.Reset()
	if err := checkCmd(ctx, w.args("thefork"), &out); err != nil {
		t.Fatalf("check after resolving: %v\n%s", err, out.String())
	}
	git(t, w.fork, "commit", "-q", "-m", "Merge upstream main into gnome-51")
	out.Reset()
	if err := pinCmd(ctx, w.args("thefork"), &out); err != nil {
		t.Fatal(err)
	}
	recipe, _ := os.ReadFile(filepath.Join(w.recipes, "thefork.yaml"))
	head := git(t, w.fork, "rev-parse", "HEAD")
	if !strings.Contains(string(recipe), "expected-commit: "+head) || !strings.Contains(string(recipe), "version: \"1\"\n  epoch: 3\n") {
		t.Errorf("the pin after a merge at the same upstream version:\n%s\n%s", recipe, out.String())
	}
	if !strings.Contains(out.String(), "push gnome-51") {
		t.Errorf("an unpushed head is noted:\n%s", out.String())
	}
	// Once more: nothing to merge.
	out.Reset()
	if err := syncCmd(ctx, w.args("thefork"), &out); err != nil || w.report(t).Result != "at-head" {
		t.Errorf("sync with nothing new: %v, %s", err, out.String())
	}
}

// Upstream's change touches what the fork never did: the merge is committed with upstream's
// subjects, and the pin takes upstream's new version at epoch 0.
func TestCleanMerge(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	write(t, w.upstream, "c.txt", "c\n")
	write(t, w.upstream, "metadata.json", "{\n  \"version\": 2,\n  \"uuid\": \"thefork@example.com\",\n  \"name\": \"The fork\",\n  \"shell-version\": [\"49\", \"50\", \"51\"]\n}\n")
	git(t, w.upstream, "add", "-A")
	git(t, w.upstream, "commit", "-q", "-m", "c: add, and v2")
	var out bytes.Buffer
	if err := syncCmd(ctx, w.args("thefork"), &out); err != nil {
		t.Fatal(err)
	}
	r := w.report(t)
	if r.Result != "merged" || r.MergeCommit != git(t, w.fork, "rev-parse", "HEAD") {
		t.Fatalf("%+v", r)
	}
	if msg := git(t, w.fork, "log", "-1", "--format=%B"); !strings.Contains(msg, "Merge upstream main into gnome-51") || !strings.Contains(msg, "c: add, and v2") {
		t.Errorf("the merge's message:\n%s", msg)
	}
	if got, _ := os.ReadFile(filepath.Join(w.fork, "metadata.json")); !strings.Contains(string(got), `["51"]`) {
		t.Errorf("the fork's shell-version was lost: %s", got)
	}
	out.Reset()
	if err := checkCmd(ctx, w.args("thefork"), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	out.Reset()
	if err := pinCmd(ctx, w.args("thefork"), &out); err != nil {
		t.Fatal(err)
	}
	recipe, _ := os.ReadFile(filepath.Join(w.recipes, "thefork.yaml"))
	if !strings.Contains(string(recipe), "version: \"2\"\n  epoch: 0\n") {
		t.Errorf("upstream's v2 not taken:\n%s", recipe)
	}
}

// Upstream released the fork's own history as one commit: the sides conflict everywhere they
// both touched, and nothing differs where only upstream did. -ours records the head.
func TestSquashedUpstream(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	// Upstream squashes the fork's two commits into one, plus a README line the fork never had.
	git(t, w.upstream, "fetch", "-q", w.fork, "gnome-51:fork")
	git(t, w.upstream, "merge", "-q", "--squash", "fork")
	write(t, w.upstream, "README.md", "release\n")
	git(t, w.upstream, "add", "-A")
	git(t, w.upstream, "commit", "-q", "-m", "version 2.0")
	var out bytes.Buffer
	if err := syncCmd(ctx, w.args("thefork"), &out); err != nil {
		t.Fatal(err)
	}
	r := w.report(t)
	// metadata.json and ci.yml and a.txt were changed identically by both: git merges those
	// cleanly; only the squash's new README is upstream's alone.
	if r.Result != "merged" || r.Untouched.Paths != 1 || r.Untouched.Differ != 1 {
		t.Fatalf("%+v", r)
	}
	git(t, w.fork, "reset", "-q", "--hard", "HEAD^1")
	out.Reset()
	if err := syncCmd(ctx, append(w.args("thefork"), "-ours"), &out); err != nil {
		t.Fatal(err)
	}
	if r := w.report(t); r.Result != "recorded" {
		t.Fatalf("%+v", r)
	}
	if diff := git(t, w.fork, "diff", "HEAD^1", "HEAD", "--stat"); diff != "" {
		t.Errorf("-ours changed the tree:\n%s", diff)
	}
	if _, err := git2(w.fork, "merge-base", "--is-ancestor", "upstream/main", "HEAD"); err != nil {
		t.Error("upstream's head is not recorded as merged")
	}
}

func git2(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
	return strings.TrimSpace(string(out)), err
}

// metadata that lists every shell fails the check; the fork's 51 alone passes.
func TestShell51(t *testing.T) {
	if err := shell51([]byte(`{"shell-version": ["50", "51"]}`)); err == nil {
		t.Error("two shells passed")
	}
	if err := shell51([]byte(`{"shell-version": ["51"]}`)); err != nil {
		t.Error(err)
	}
	if err := shell51([]byte(`{"version": 3}`)); err != nil {
		t.Error("metadata without shell-version is not an extension's:", err)
	}
}
