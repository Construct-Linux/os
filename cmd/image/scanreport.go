package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Construct-Linux/os/internal/grype"
)

// severities in the order a report lists them.
var severities = []string{"Critical", "High", "Medium", "Low", "Negligible", "Unknown"}

// scanReport writes what task scan's Grype found as Markdown, by severity: the vulnerabilities a
// version already fixes - Critical and High fail the scan (build/grype.yaml) - and those nobody
// has fixed yet, which Grype keeps under ignoredMatches. Each says whether the package is Wolfi's,
// fixed by rebuilding the root, or one of recipes/, fixed by updating its recipe.
func scanReport(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("scan-report takes no arguments")
	}
	// Where task scan leaves Grype's JSON, and the Markdown beside it.
	in := filepath.Join(out, "scan", "grype.json")
	to := filepath.Join(out, "scan", "report.md")
	b, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var g grype.Report
	if err := json.Unmarshal(b, &g); err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	ours, err := grype.RecipePackages("recipes")
	if err != nil {
		return err
	}
	var md strings.Builder
	writeScanReport(&md, g, ours)
	if err := os.WriteFile(to, []byte(md.String()), 0o644); err != nil {
		return err
	}
	// The counts, where task scan's output shows them.
	summary, _, _ := strings.Cut(md.String(), "\n## ")
	fmt.Print(summary)
	fmt.Printf("\nThe whole report: %s\n", to)
	return nil
}

func writeScanReport(w io.Writer, g grype.Report, ours map[string]bool) {
	fmt.Fprintln(w, "# Vulnerabilities in the image")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| severity | a version fixes it | nobody has yet |")
	fmt.Fprintln(w, "|---|---:|---:|")
	fixed, unfixed := bySeverity(g.Matches), bySeverity(g.IgnoredMatches)
	for _, s := range severities {
		fmt.Fprintf(w, "| %s | %d | %d |\n", s, len(fixed[s]), len(unfixed[s]))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "A Critical or High that a version fixes fails `task scan`: a Wolfi package's by rebuilding the root, a recipe's by updating it.")
	for _, part := range []struct {
		title string
		by    map[string][]grype.Match
	}{{"A version fixes it", fixed}, {"Nobody has fixed it yet", unfixed}} {
		for _, s := range severities {
			if len(part.by[s]) == 0 {
				continue
			}
			fmt.Fprintf(w, "\n## %s: %s\n\n", part.title, s)
			fmt.Fprintln(w, "| vulnerability | package | installed | fixed in | from | EPSS |")
			fmt.Fprintln(w, "|---|---|---|---|---|---:|")
			for _, m := range part.by[s] {
				from := "Wolfi"
				if ours[m.Artifact.Name] {
					from = "recipes/"
				}
				epss := ""
				if len(m.Vulnerability.EPSS) > 0 {
					epss = fmt.Sprintf("%.2f%%", 100*m.Vulnerability.EPSS[0].EPSS)
				}
				fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s |\n", m.Vulnerability.ID, m.Artifact.Name, m.Artifact.Version,
					strings.Join(m.Vulnerability.Fix.Versions, ", "), from, epss)
			}
		}
	}
}

// bySeverity groups matches by Grype's severity, each group by the likeliest to be exploited
// first (EPSS), then by name.
func bySeverity(ms []grype.Match) map[string][]grype.Match {
	by := map[string][]grype.Match{}
	for _, m := range ms {
		s := m.Vulnerability.Severity
		if !slices.Contains(severities, s) {
			s = "Unknown"
		}
		by[s] = append(by[s], m)
	}
	for _, list := range by {
		sort.SliceStable(list, func(i, j int) bool {
			if a, b := list[i].EPSS(), list[j].EPSS(); a != b {
				return a > b
			}
			return list[i].Artifact.Name+list[i].Vulnerability.ID < list[j].Artifact.Name+list[j].Vulnerability.ID
		})
	}
	return by
}
