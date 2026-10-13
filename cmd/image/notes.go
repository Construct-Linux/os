package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/grype"
)

// A release's notes, written from what the build already knows, so every release reads the same
// way and nothing in them is typed by hand: what changed (the commits, by area), which packages
// moved (against the previous release's packages.txt), what Grype found, and what the harness
// checked. A person, or the release skill, adds the highlights above them afterwards.

// notesInput is everything the notes are written from.
type notesInput struct {
	Version, Prev string
	Date          time.Time
	// Commits are the subjects since Prev, newest first.
	Commits []string
	// Packages and PrevPackages are name => version; PrevPackages is nil for a first release.
	Packages, PrevPackages map[string]string
	Grype                  *grype.Report
	Tests                  []testRun
	// DLURL is where the USB sticks are (dl.<domain>).
	DLURL string
}

// testRun is one harness report: how many checks passed, and the failures by name.
type testRun struct {
	Name          string
	Passed, Total int
	Failed        []string
}

func releaseNotes(args []string) error {
	fl := flag.NewFlagSet("release-notes", flag.ExitOnError)
	version := fl.String("version", "", "the release's version, without the v")
	prev := fl.String("prev", "", "the previous release's tag (empty: the first release)")
	packages := fl.String("packages", "", "this release's packages.txt")
	prevPackages := fl.String("prev-packages", "", "the previous release's packages.txt")
	dl := fl.String("dl-url", os.Getenv("CONSTRUCT_DL_URL"), "where the USB sticks are (dl.<domain>)")
	to := fl.String("o", filepath.Join(out, "release-notes", "notes.md"), "the notes to write")
	_ = fl.Parse(args)
	if *version == "" || *packages == "" {
		return fmt.Errorf("release-notes: -version and -packages are required")
	}
	in := notesInput{Version: *version, Prev: *prev, Date: time.Now().UTC(), DLURL: *dl}
	var err error
	if in.Commits, err = commitsSince(*prev); err != nil {
		return err
	}
	if in.Packages, err = readPackages(*packages); err != nil {
		return err
	}
	if *prevPackages != "" {
		if in.PrevPackages, err = readPackages(*prevPackages); err != nil {
			return err
		}
	}
	// Grype's report of the release's root, and the harness's, where task scan and the tests left
	// them on the builder.
	grypeFile := filepath.Join(out, "scan", "grype.json")
	if b, err := os.ReadFile(grypeFile); err == nil {
		var g grype.Report
		if err := json.Unmarshal(b, &g); err != nil {
			return fmt.Errorf("%s: %w", grypeFile, err)
		}
		in.Grype = &g
	}
	for _, f := range []string{filepath.Join(out, "test", "report.txt"), filepath.Join(out, "lifecycle", "report.txt")} {
		if r, err := readTestRun(f); err == nil {
			in.Tests = append(in.Tests, r)
		}
	}
	if err := os.MkdirAll(filepath.Dir(*to), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	writeNotes(&b, in)
	return os.WriteFile(*to, []byte(b.String()), 0o644)
}

// commitsSince are the subjects of the commits after tag, or of the last 50 for a first release.
func commitsSince(tag string) ([]string, error) {
	args := []string{"log", "--no-merges", "--format=%s"}
	if tag != "" {
		args = append(args, tag+"..HEAD")
	} else {
		args = append(args, "-50")
	}
	b, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	return strings.FieldsFunc(string(b), func(r rune) bool { return r == '\n' }), nil
}

// writePackages writes the root's installed packages as packages.txt: "name version", sorted,
// which the next release's notes compare against.
func writePackages(installed, to string) error {
	f, err := os.Open(installed)
	if err != nil {
		return err
	}
	defer f.Close()
	pkgs, err := parseInstalled(f)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(pkgs))
	for n := range pkgs {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s %s\n", n, pkgs[n])
	}
	return os.WriteFile(to, []byte(b.String()), 0o644)
}

// parseInstalled is apk's installed database as each package's version, by name.
func parseInstalled(r io.Reader) (map[string]string, error) {
	db, err := readAPKDB(r)
	pkgs := map[string]string{}
	for _, p := range db {
		pkgs[p.Name] = p.Version
	}
	return pkgs, err
}

func readPackages(file string) (map[string]string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	pkgs := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if n, v, ok := strings.Cut(line, " "); ok {
			pkgs[n] = v
		}
	}
	return pkgs, nil
}

// readTestRun reads a harness report.txt: its PASS and FAIL lines and its summary.
func readTestRun(file string) (testRun, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return testRun{}, err
	}
	r := testRun{Name: filepath.Base(filepath.Dir(file))}
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "PASS "):
			r.Passed++
			r.Total++
		case strings.HasPrefix(line, "FAIL "):
			r.Total++
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "FAIL "), ":")
			r.Failed = append(r.Failed, strings.TrimSpace(name))
		}
	}
	return r, nil
}

// pkgChange is a package that moved between two releases.
type pkgChange struct{ Name, From, To string }

// diffPackages are the packages added, removed and updated from prev to cur, each by name.
func diffPackages(prev, cur map[string]string) (added, removed, updated []pkgChange) {
	for n, v := range cur {
		switch old, ok := prev[n]; {
		case !ok:
			added = append(added, pkgChange{n, "", v})
		case old != v:
			updated = append(updated, pkgChange{n, old, v})
		}
	}
	for n, v := range prev {
		if _, ok := cur[n]; !ok {
			removed = append(removed, pkgChange{n, v, ""})
		}
	}
	for _, l := range [][]pkgChange{added, removed, updated} {
		sort.Slice(l, func(i, j int) bool { return l[i].Name < l[j].Name })
	}
	return added, removed, updated
}

// byArea groups commit subjects by their area (the "area:" a subject starts with), the areas with
// the most commits first.
func byArea(subjects []string) (areas []string, by map[string][]string) {
	by = map[string][]string{}
	for _, s := range subjects {
		area, what, ok := strings.Cut(s, ": ")
		// An area is a word or a few ("image, harness"); a colon later in a sentence is not one.
		if !ok || len(area) > 32 {
			area, what = "other", s
		}
		by[area] = append(by[area], what)
	}
	for a := range by {
		areas = append(areas, a)
	}
	sort.Slice(areas, func(i, j int) bool {
		if len(by[areas[i]]) != len(by[areas[j]]) {
			return len(by[areas[i]]) > len(by[areas[j]])
		}
		return areas[i] < areas[j]
	})
	return areas, by
}

func writeNotes(w io.Writer, in notesInput) {
	fmt.Fprintf(w, "# CONSTRUCT %s\n\n", in.Version)
	fmt.Fprintf(w, "Built %s", in.Date.Format("2006-01-02"))
	if k, ok := in.Packages["linux"]; ok {
		fmt.Fprintf(w, " · kernel %s", k)
	}
	if g, ok := in.Packages["gnome-shell"]; ok {
		fmt.Fprintf(w, " · GNOME Shell %s", g)
	}
	if m, ok := in.Packages["mesa"]; ok {
		fmt.Fprintf(w, " · Mesa %s", m)
	}
	fmt.Fprint(w, "\n\n")

	fmt.Fprintln(w, "## Changes")
	fmt.Fprintln(w)
	if in.Prev == "" {
		fmt.Fprintf(w, "The first release. Its last %d changes:\n\n", len(in.Commits))
	} else {
		fmt.Fprintf(w, "%d changes since %s.\n\n", len(in.Commits), in.Prev)
	}
	areas, by := byArea(in.Commits)
	for _, a := range areas {
		fmt.Fprintf(w, "**%s**\n", a)
		for _, s := range by[a] {
			fmt.Fprintf(w, "- %s\n", s)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "## Packages")
	fmt.Fprintln(w)
	if in.PrevPackages == nil {
		fmt.Fprintf(w, "%d packages; `packages.txt` lists them.\n\n", len(in.Packages))
	} else {
		added, removed, updated := diffPackages(in.PrevPackages, in.Packages)
		fmt.Fprintf(w, "%d packages: %d updated, %d added, %d removed.\n\n", len(in.Packages), len(updated), len(added), len(removed))
		writeChanges(w, "Updated", updated, func(c pkgChange) string { return c.From + " → " + c.To })
		writeChanges(w, "Added", added, func(c pkgChange) string { return c.To })
		writeChanges(w, "Removed", removed, func(c pkgChange) string { return c.From })
	}

	if in.Grype != nil {
		fmt.Fprintln(w, "## Security")
		fmt.Fprintln(w)
		fixed, unfixed := bySeverity(in.Grype.Matches), bySeverity(in.Grype.IgnoredMatches)
		fmt.Fprintln(w, "Grype over the release's root, on the day it was built:")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "| severity | a version fixes it | nobody has yet |")
		fmt.Fprintln(w, "|---|---:|---:|")
		for _, s := range severities {
			fmt.Fprintf(w, "| %s | %d | %d |\n", s, len(fixed[s]), len(unfixed[s]))
		}
		fmt.Fprintln(w)
		var urgent []grype.Match
		for _, s := range []string{"Critical", "High"} {
			urgent = append(urgent, fixed[s]...)
		}
		if len(urgent) > 0 {
			fmt.Fprintln(w, "| vulnerability | severity | package | installed | fixed in |")
			fmt.Fprintln(w, "|---|---|---|---|---|")
			for _, m := range urgent {
				fmt.Fprintf(w, "| %s | %s | %s | %s | %s |\n", m.Vulnerability.ID, m.Vulnerability.Severity,
					m.Artifact.Name, m.Artifact.Version, strings.Join(m.Vulnerability.Fix.Versions, ", "))
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, "The whole report is `grype.md`; Grype's own, `grype.json`; the packages, `sbom.spdx.json`.")
		fmt.Fprintln(w)
	}

	if len(in.Tests) > 0 {
		fmt.Fprintln(w, "## Checked")
		fmt.Fprintln(w)
		for _, t := range in.Tests {
			fmt.Fprintf(w, "- **%s**: %d of %d checks passed", t.Name, t.Passed, t.Total)
			if len(t.Failed) > 0 {
				fmt.Fprintf(w, "; failed: %s", strings.Join(t.Failed, "; "))
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "## Get it")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "An installed machine takes it by itself from its channel, and starts it at its next reboot.")
	if in.DLURL != "" {
		fmt.Fprintf(w, "A new machine: the USB stick, %s/construct_%s.raw.xz, in the signed %s/SHA256SUMS; how to check it and write it to a stick is the README's Install.\n", in.DLURL, in.Version, in.DLURL)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Every file is in `SHA256SUMS`, signed with the release key (`release.pgp`):")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "```sh")
	fmt.Fprintln(w, "gpg --import release.pgp && gpg --verify SHA256SUMS.gpg SHA256SUMS && sha256sum -c --ignore-missing SHA256SUMS")
	fmt.Fprintln(w, "```")
}

// writeChanges lists a kind of package change, folded when it is long.
func writeChanges(w io.Writer, title string, l []pkgChange, what func(pkgChange) string) {
	if len(l) == 0 {
		return
	}
	fold := len(l) > 15
	if fold {
		fmt.Fprintf(w, "<details><summary>%s (%d)</summary>\n\n", title, len(l))
	} else {
		fmt.Fprintf(w, "**%s**\n\n", title)
	}
	for _, c := range l {
		fmt.Fprintf(w, "- %s %s\n", c.Name, what(c))
	}
	if fold {
		fmt.Fprint(w, "\n</details>")
	}
	fmt.Fprint(w, "\n\n")
}
