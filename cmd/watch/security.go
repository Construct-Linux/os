package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/Construct-Linux/os/internal/grype"
)

// kernelPackages are the names Grype gives the kernel it finds in an SBOM.
var kernelPackages = map[string]bool{"linux": true, "linux-kernel": true}

// securityCheck finds, in Grype's report of the latest release's SBOM, each package with a
// Critical or High vulnerability a newer version fixes: one issue a package, whatever the number
// of its vulnerabilities. One nobody has fixed has nothing to update to and is no issue.
func securityCheck(path, release, recipes string) check {
	return check{
		scope: "security",
		gone:  "The latest release's SBOM has no Critical or High vulnerability a version fixes in this package any more: closing.",
		find: func(context.Context) ([]finding, error) {
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			var r grype.Report
			if err := json.Unmarshal(b, &r); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			ours, err := grype.RecipePackages(recipes)
			if err != nil {
				return nil, err
			}
			return securityFindings(r, release, ours), nil
		},
	}
}

func securityFindings(r grype.Report, release string, ours map[string]bool) []finding {
	byPackage := map[string][]grype.Match{}
	// What a rule of build/grype.yaml keeps is an issue too: the rule is dropped when a release
	// of the binary's vendor carries the fix, and the issue is where that is waited for.
	matches := slices.Clone(r.Matches)
	for _, m := range r.IgnoredMatches {
		if m.Kept() != "" {
			matches = append(matches, m)
		}
	}
	for _, m := range matches {
		s := m.Vulnerability.Severity
		if (s != "Critical" && s != "High") || len(m.Vulnerability.Fix.Versions) == 0 {
			continue
		}
		name := m.Artifact.Name
		// Grype can match one vulnerability twice, by two of its matchers.
		dup := false
		for _, o := range byPackage[name] {
			dup = dup || o.Vulnerability.ID == m.Vulnerability.ID
		}
		if !dup {
			byPackage[name] = append(byPackage[name], m)
		}
	}
	names := make([]string, 0, len(byPackage))
	for n := range byPackage {
		names = append(names, n)
	}
	sort.Strings(names)
	var found []finding
	for _, name := range names {
		ms := byPackage[name]
		sort.SliceStable(ms, func(i, j int) bool {
			if a, b := ms[i].Vulnerability.Severity == "Critical", ms[j].Vulnerability.Severity == "Critical"; a != b {
				return a
			}
			if a, b := ms[i].EPSS(), ms[j].EPSS(); a != b {
				return a > b
			}
			return ms[i].Vulnerability.ID < ms[j].Vulnerability.ID
		})
		severity := ms[0].Vulnerability.Severity
		var versions []string
		for _, m := range ms {
			if !slices.Contains(versions, m.Artifact.Version) {
				versions = append(versions, m.Artifact.Version)
			}
		}
		version := strings.Join(versions, ", ")
		area := "area:packages"
		if kernelPackages[name] {
			area = "area:kernel"
		}
		var b strings.Builder
		of := "The latest release's SBOM"
		if release != "" {
			of = release + "'s SBOM"
		}
		fmt.Fprintf(&b, "%s has %d Critical or High vulnerabilities in `%s` %s that a newer version fixes.\n\n", of, len(ms), name, version)
		fmt.Fprintln(&b, "| CVE | severity | fixed in | EPSS |")
		fmt.Fprintln(&b, "|---|---|---|---:|")
		for _, m := range ms {
			epss := ""
			if len(m.Vulnerability.EPSS) > 0 {
				epss = fmt.Sprintf("%.2f%%", 100*m.EPSS())
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", m.Vulnerability.ID, m.Vulnerability.Severity, strings.Join(m.Vulnerability.Fix.Versions, ", "), epss)
		}
		fmt.Fprintln(&b)
		switch {
		case ms[0].Kept() != "":
			fmt.Fprintf(&b, "A rule in build/grype.yaml keeps it out of `task scan`: %s. Drop the rule when a release carries the fix.\n", ms[0].Kept())
		case kernelPackages[name]:
			fmt.Fprintln(&b, "The kernel is built here: bump KERNEL_VERSION and KERNEL_SHA256 in build/versions.env to a fixed release.")
		case ours[name]:
			fmt.Fprintf(&b, "`%s` is built by recipes/: update its recipe to a fixed version.\n", name)
		default:
			fmt.Fprintf(&b, "`%s` is Wolfi's, and Wolfi has published the fix: `task lock -- -all` takes it into image/apko.lock.yaml.\n", name)
		}
		found = append(found, finding{
			key:    "security:" + name,
			title:  fmt.Sprintf("security: %s %s - %d fixable (%s)", name, version, len(ms), severity),
			labels: []string{"security", "severity:" + strings.ToLower(severity), area},
			body:   b.String(),
		})
	}
	return found
}
