package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Construct-Linux/os/internal/grype"
)

// The counts split what a version fixes from what nobody has and from what a rule of
// build/grype.yaml keeps, with the rule's reason; each row of the first two says whose package it
// is: a recipe's is updated, a Wolfi package's is fixed by rebuilding the root.
func TestScanReport(t *testing.T) {
	var g grype.Report
	if err := json.Unmarshal([]byte(`{
		"matches": [{"vulnerability": {"id": "CVE-1", "severity": "High", "fix": {"versions": ["2.0"]}}, "artifact": {"name": "mesa", "version": "1.0"}}],
		"ignoredMatches": [
			{"vulnerability": {"id": "CVE-2", "severity": "Medium", "epss": [{"epss": 0.01}]}, "artifact": {"name": "glibc-2.44", "version": "2.44"}},
			{"vulnerability": {"id": "CVE-3", "severity": "Medium", "epss": [{"epss": 0.5}]}, "artifact": {"name": "glibc-2.44", "version": "2.44"}},
			{"vulnerability": {"id": "CVE-4", "severity": "Weird"}, "artifact": {"name": "x", "version": "1"}},
			{"vulnerability": {"id": "GHSA-5", "severity": "High", "fix": {"versions": ["9.0.21"]}}, "artifact": {"name": "Pkcs", "version": "9.0.5"},
			 "appliedIgnoreRules": [{"vulnerability": "GHSA-5", "reason": "only its vendor rebuilds it"}]}
		]}`), &g); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	writeScanReport(&b, g, map[string]bool{"mesa": true})
	md := b.String()
	for _, want := range []string{
		"| High | 1 | 0 | 1 |", "| Medium | 0 | 2 | 0 |", "| Unknown | 0 | 1 | 0 |",
		"| CVE-1 | mesa | 1.0 | 2.0 | recipes/ |",
		"| CVE-2 | glibc-2.44 | 2.44 |  | Wolfi | 1.00% |",
		"## Kept by a rule in build/grype.yaml: High", "| GHSA-5 | Pkcs | 9.0.5 | 9.0.21 | only its vendor rebuilds it |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in\n%s", want, md)
		}
	}
	// The likeliest to be exploited first.
	if strings.Index(md, "CVE-3") > strings.Index(md, "CVE-2") {
		t.Errorf("CVE-3 (EPSS 50%%) should come before CVE-2 (1%%)")
	}
}
