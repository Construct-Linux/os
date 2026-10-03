package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// severities in the order a report lists them.
var severities = []string{"Critical", "High", "Medium", "Low", "Negligible", "Unknown"}

// grypeMatch is what a report needs of one of Grype's matches.
type grypeMatch struct {
	Vulnerability struct {
		ID       string `json:"id"`
		Severity string `json:"severity"`
		Fix      struct {
			Versions []string `json:"versions"`
		} `json:"fix"`
		EPSS []struct {
			EPSS       float64 `json:"epss"`
			Percentile float64 `json:"percentile"`
		} `json:"epss"`
	} `json:"vulnerability"`
	Artifact struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"artifact"`
}

type grypeReport struct {
	Matches        []grypeMatch `json:"matches"`
	IgnoredMatches []grypeMatch `json:"ignoredMatches"`
}

// scanReport writes what task scan's Grype found as Markdown, by severity: the vulnerabilities a
// version already fixes - Critical and High fail the scan (build/grype.yaml) - and those nobody
// has fixed yet, which Grype keeps under ignoredMatches. Each says whether the package is Wolfi's,
// fixed by rebuilding the root, or one of recipes/, fixed by updating its recipe.
func scanReport(args []string) error {
	fl := flag.NewFlagSet("scan-report", flag.ExitOnError)
	in := fl.String("json", filepath.Join(out, "scan", "grype.json"), "Grype's JSON report")
	to := fl.String("o", filepath.Join(out, "scan", "report.md"), "the Markdown report to write")
	recipes := fl.String("recipes", "recipes", "the recipes' directory")
	_ = fl.Parse(args)
	b, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	var g grypeReport
	if err := json.Unmarshal(b, &g); err != nil {
		return fmt.Errorf("%s: %w", *in, err)
	}
	ours, err := recipePackages(*recipes)
	if err != nil {
		return err
	}
	var md strings.Builder
	writeScanReport(&md, g, ours)
	if err := os.WriteFile(*to, []byte(md.String()), 0o644); err != nil {
		return err
	}
	// The counts, where task scan's output shows them.
	summary, _, _ := strings.Cut(md.String(), "\n## ")
	fmt.Print(summary)
	fmt.Printf("\nThe whole report: %s\n", *to)
	return nil
}

func writeScanReport(w io.Writer, g grypeReport, ours map[string]bool) {
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
		by    map[string][]grypeMatch
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
func bySeverity(ms []grypeMatch) map[string][]grypeMatch {
	by := map[string][]grypeMatch{}
	for _, m := range ms {
		s := m.Vulnerability.Severity
		if !contains(severities, s) {
			s = "Unknown"
		}
		by[s] = append(by[s], m)
	}
	epss := func(m grypeMatch) float64 {
		if len(m.Vulnerability.EPSS) == 0 {
			return 0
		}
		return m.Vulnerability.EPSS[0].EPSS
	}
	for _, list := range by {
		sort.SliceStable(list, func(i, j int) bool {
			if a, b := epss(list[i]), epss(list[j]); a != b {
				return a > b
			}
			return list[i].Artifact.Name+list[i].Vulnerability.ID < list[j].Artifact.Name+list[j].Vulnerability.ID
		})
	}
	return by
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var recipeName = regexp.MustCompile(`(?m)^(?:  name|  - name): *"?([A-Za-z0-9._+-]+)"?\s*$`)

// recipePackages are the packages recipes/ builds: each recipe's package and subpackages.
func recipePackages(dir string) (map[string]bool, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, m := range recipeName.FindAllSubmatch(b, -1) {
			names[string(m[1])] = true
		}
	}
	return names, nil
}
