package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Construct-Linux/os/internal/forks"
)

// checkCmd says whether the fork's tree holds after a merge or a resolution: no conflict left,
// every JavaScript file parses, the schemas compile, JSON and XML parse, metadata declares GNOME
// Shell 51 alone, and the recipe's patches still apply. It judges nothing: a wrong resolution
// that parses passes here and fails in the harness.
func checkCmd(ctx context.Context, args []string, out io.Writer) error {
	fl := flag.NewFlagSet("check", flag.ContinueOnError)
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
	var fails, notes []string
	fail := func(format string, a ...any) { fails = append(fails, fmt.Sprintf(format, a...)) }
	note := func(format string, a ...any) { notes = append(notes, fmt.Sprintf(format, a...)) }

	if unmerged, _ := r.lines(ctx, "diff", "--name-only", "--diff-filter=U"); len(unmerged) > 0 {
		fail("unresolved in the index: %s (git add each once resolved)", strings.Join(unmerged, ", "))
	}
	// git grep knows a binary file from a text one; a lone ======= is a Markdown underline.
	if marked, _ := r.lines(ctx, "grep", "-l", "-E", `^(<<<<<<< |>>>>>>> )`); len(marked) > 0 {
		fail("conflict markers left in: %s", strings.Join(marked, ", "))
	}
	files, err := r.lines(ctx, "ls-files")
	if err != nil {
		return err
	}
	schemaDirs := map[string]bool{}
	var tsFiles []string
	for _, f := range files {
		path := filepath.Join(r.dir, f)
		if strings.Contains(f, "node_modules/") || strings.HasPrefix(f, "dist/") {
			continue
		}
		// An icon theme's links/ are symlinks resolved where the theme is installed; tsconfig
		// is JSON with comments, which the build reads and encoding/json does not.
		if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink != 0 || strings.HasPrefix(filepath.Base(f), "tsconfig") {
			continue
		}
		switch {
		case strings.HasSuffix(f, ".js"):
			if err := parseJS(ctx, path); err != nil {
				fail("%s: %v", f, err)
			}
		case strings.HasSuffix(f, ".json"):
			b, err := os.ReadFile(path)
			if err != nil || !json.Valid(b) {
				fail("%s: not valid JSON", f)
			} else if filepath.Base(f) == "metadata.json" {
				if err := shell51(b); err != nil {
					fail("%s: %v", f, err)
				}
			}
		case strings.HasSuffix(f, ".ts") && !strings.HasSuffix(f, ".d.ts"):
			tsFiles = append(tsFiles, f)
		case strings.HasSuffix(f, ".ui"), strings.HasSuffix(f, ".xml"), strings.HasSuffix(f, ".svg"):
			if err := parseXML(path); err != nil {
				fail("%s: %v", f, err)
			}
			if strings.HasSuffix(f, ".gschema.xml") {
				schemaDirs[filepath.Dir(path)] = true
			}
		}
	}
	for dir := range schemaDirs {
		if _, err := exec.LookPath("glib-compile-schemas"); err != nil {
			fail("glib-compile-schemas is not installed, needed for %s", dir)
			continue
		}
		c := exec.CommandContext(ctx, "glib-compile-schemas", "--strict", "--dry-run", dir)
		if b, err := c.CombinedOutput(); err != nil {
			fail("%s: %s", dir, strings.TrimSpace(string(b)))
		}
	}
	for _, f := range tsFiles {
		// A TypeScript project builds with esbuild, which bundles without checking types; the
		// check parses each file with the project's own esbuild, as the build will. Types are
		// not checked: the fork's build never did.
		esbuild := filepath.Join(r.dir, "node_modules", ".bin", "esbuild")
		if _, err := os.Stat(esbuild); err != nil {
			note("node_modules/.bin/esbuild missing, %s not parsed (npm ci first)", f)
			continue
		}
		c := exec.CommandContext(ctx, esbuild, "--log-level=error", "--format=esm", filepath.Join(r.dir, f))
		c.Stdout = io.Discard
		var stderr strings.Builder
		c.Stderr = &stderr
		if err := c.Run(); err != nil {
			fail("%s: does not parse:\n%s", f, strings.TrimSpace(stderr.String()))
		}
	}
	for _, patch := range recipePatches(pin) {
		// git apply runs in the checkout: the patch is named from there.
		if abs, err := filepath.Abs(patch); err == nil {
			patch = abs
		}
		if _, err := os.Stat(patch); err != nil {
			fail("%s: %v", patch, err)
			continue
		}
		if _, err := r.git(ctx, "apply", "--check", patch); err != nil {
			fail("the recipe's patch %s no longer applies: %v", filepath.Base(patch), err)
		}
	}
	for _, f := range readded(ctx, r) {
		note("%s: the fork had removed it, the merge brought it back", f)
	}
	for _, n := range notes {
		fmt.Fprintln(out, "note:", n)
	}
	for _, f := range fails {
		fmt.Fprintln(out, "FAIL:", f)
	}
	if len(fails) > 0 {
		return fmt.Errorf("%d check(s) failed", len(fails))
	}
	fmt.Fprintf(out, "%s: %d files hold\n", pin.Name, len(files))
	return nil
}

// parseJS parses one file with node: the extensions are ES modules, which node reads as such from
// a .mjs name, so the file is copied under one.
func parseJS(ctx context.Context, path string) error {
	if _, err := exec.LookPath("node"); err != nil {
		return errors.New("node is not installed")
	}
	tmp, err := os.CreateTemp("", "fork-*.mjs")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	_ = tmp.Close()
	c := exec.CommandContext(ctx, "node", "--check", tmp.Name())
	if out, err := c.CombinedOutput(); err != nil {
		msg := strings.ReplaceAll(strings.TrimSpace(string(out)), tmp.Name(), filepath.Base(path))
		return fmt.Errorf("does not parse:\n%s", msg)
	}
	return nil
}

func parseXML(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := xml.NewDecoder(f)
	d.Strict = true
	for {
		if _, err := d.Token(); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("not well-formed XML: %v", err)
		}
	}
}

// shell51 checks an extension's metadata declares GNOME Shell 51 and nothing else: the forks
// build for 51 alone, and upstream's list of every shell comes back with each merge.
func shell51(metadata []byte) error {
	var m struct {
		ShellVersion []string `json:"shell-version"`
	}
	if err := json.Unmarshal(metadata, &m); err != nil {
		return err
	}
	if m.ShellVersion == nil {
		return nil
	}
	if !slices.Equal(m.ShellVersion, []string{"51"}) {
		return fmt.Errorf("shell-version is %v, the fork declares [\"51\"] alone", m.ShellVersion)
	}
	return nil
}

var patchLine = regexp.MustCompile(`(?m)^\s+patches:\s*(\S+)`)

// recipePatches are the patches the recipe applies over the checkout, relative to the recipes
// directory, as melange's patch pipeline reads them.
func recipePatches(pin forks.Pin) []string {
	b, err := os.ReadFile(pin.Recipe)
	if err != nil {
		return nil
	}
	var paths []string
	for _, m := range patchLine.FindAllStringSubmatch(string(b), -1) {
		for _, p := range strings.Fields(m[1]) {
			paths = append(paths, filepath.Join(filepath.Dir(pin.Recipe), p))
		}
	}
	return paths
}

// readded are the files the fork had deleted since the base that the merge, in progress or just
// committed, brought back: upstream's CI, templates and tooling the fork removed on purpose.
func readded(ctx context.Context, r repo) []string {
	ours, theirs := "HEAD", "MERGE_HEAD"
	if _, err := r.git(ctx, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err != nil {
		// No merge in progress: HEAD may be the merge commit itself.
		parents, err := r.lines(ctx, "rev-list", "--parents", "-n", "1", "HEAD")
		if err != nil || len(parents) == 0 || len(strings.Fields(parents[0])) != 3 {
			return nil
		}
		ours, theirs = "HEAD^1", "HEAD^2"
	}
	base, err := r.git(ctx, "merge-base", ours, theirs)
	if err != nil {
		return nil
	}
	deleted, _ := r.lines(ctx, "diff", "--name-only", "--diff-filter=D", base, ours)
	var back []string
	for _, f := range deleted {
		if _, err := os.Stat(filepath.Join(r.dir, f)); err == nil {
			back = append(back, f)
		}
	}
	return back
}
