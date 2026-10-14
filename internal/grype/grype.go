// Package grype is what is read of a Grype JSON report, and of recipes/ to say whose package a
// vulnerable one is: task scan's report (cmd/image) and the weekly watch (cmd/watch) both read it.
package grype

import (
	"os"
	"path/filepath"
	"regexp"
)

// Match is what is needed of one of Grype's matches.
type Match struct {
	Vulnerability struct {
		ID       string `json:"id"`
		Severity string `json:"severity"`
		Fix      struct {
			Versions []string `json:"versions"`
		} `json:"fix"`
		EPSS []struct {
			EPSS float64 `json:"epss"`
		} `json:"epss"`
	} `json:"vulnerability"`
	Artifact struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"artifact"`
	// AppliedIgnoreRules is why Grype ignored the match: only-fixed's rule, which names no
	// vulnerability, or a rule of build/grype.yaml, which names one and says why.
	AppliedIgnoreRules []struct {
		Vulnerability string `json:"vulnerability"`
		Reason        string `json:"reason"`
	} `json:"appliedIgnoreRules"`
}

// Kept is the reason a rule of build/grype.yaml keeps m, "" when none does: a fix exists in a
// library a third party's binary embeds, which only that party rebuilds.
func (m Match) Kept() string {
	for _, r := range m.AppliedIgnoreRules {
		if r.Vulnerability != "" {
			return r.Reason
		}
	}
	return ""
}

// Report is Grype's JSON report: the matches a version fixes, and under ignoredMatches those
// build/grype.yaml ignores - nobody has fixed them yet, or a rule keeps them (Match.Kept).
type Report struct {
	Matches        []Match `json:"matches"`
	IgnoredMatches []Match `json:"ignoredMatches"`
}

// EPSS is the likelihood m is exploited in the next 30 days, 0 when Grype does not know it.
func (m Match) EPSS() float64 {
	if len(m.Vulnerability.EPSS) == 0 {
		return 0
	}
	return m.Vulnerability.EPSS[0].EPSS
}

var recipeName = regexp.MustCompile(`(?m)^(?:  name|  - name): *"?([A-Za-z0-9._+-]+)"?\s*$`)

// RecipePackages are the packages recipes/ builds: each recipe's package and subpackages.
func RecipePackages(dir string) (map[string]bool, error) {
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
