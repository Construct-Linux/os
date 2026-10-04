package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Construct-Linux/os/internal/grype"
)

// serve answers each path with its body: kernel.org, GNOME's downloads and GitHub, in a test.
func serve(t *testing.T, bodies map[string]string) sources {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(b))
	}))
	t.Cleanup(srv.Close)
	return sources{client: srv.Client(), kernel: srv.URL + "/releases.json", gnome: srv.URL + "/cache.json", github: srv.URL}
}

func TestSecurityFindings(t *testing.T) {
	var r grype.Report
	if err := json.Unmarshal([]byte(`{"matches": [
		{"vulnerability": {"id": "CVE-1", "severity": "High", "fix": {"versions": ["3.5.1"]}, "epss": [{"epss": 0.02}]}, "artifact": {"name": "openssl", "version": "3.5.0"}},
		{"vulnerability": {"id": "CVE-2", "severity": "Critical", "fix": {"versions": ["3.5.1"]}}, "artifact": {"name": "openssl", "version": "3.5.0"}},
		{"vulnerability": {"id": "CVE-2", "severity": "Critical", "fix": {"versions": ["3.5.1"]}}, "artifact": {"name": "openssl", "version": "3.5.0"}},
		{"vulnerability": {"id": "CVE-3", "severity": "Medium", "fix": {"versions": ["2"]}}, "artifact": {"name": "zlib", "version": "1"}},
		{"vulnerability": {"id": "CVE-4", "severity": "High", "fix": {"versions": []}}, "artifact": {"name": "glibc", "version": "2.44"}},
		{"vulnerability": {"id": "CVE-5", "severity": "High", "fix": {"versions": ["7.2.9"]}}, "artifact": {"name": "linux-kernel", "version": "7.2.8"}},
		{"vulnerability": {"id": "CVE-6", "severity": "High", "fix": {"versions": ["51.1"]}}, "artifact": {"name": "gnome-shell", "version": "51.0"}}
	]}`), &r); err != nil {
		t.Fatal(err)
	}
	fs := securityFindings(r, "v20261004.1", map[string]bool{"gnome-shell": true})
	by := map[string]finding{}
	for _, f := range fs {
		by[f.key] = f
	}
	if len(fs) != 3 {
		t.Fatalf("%d findings, want openssl, the kernel and gnome-shell (a Medium and an unfixed High are none): %+v", len(fs), fs)
	}
	o := by["security:openssl"]
	if o.title != "security: openssl 3.5.0 - 2 fixable (Critical)" || strings.Join(o.labels, ",") != "security,severity:critical,area:packages" {
		t.Errorf("openssl: %q %v", o.title, o.labels)
	}
	if !strings.Contains(o.body, "| CVE-2 | Critical | 3.5.1 |  |\n| CVE-1 | High | 3.5.1 | 2.00% |") || !strings.Contains(o.body, "Wolfi's") || !strings.Contains(o.body, "v20261004.1") {
		t.Errorf("openssl's body:\n%s", o.body)
	}
	if k := by["security:linux-kernel"]; strings.Join(k.labels, ",") != "security,severity:high,area:kernel" {
		t.Errorf("the kernel's labels: %v", k.labels)
	}
	if g := by["security:gnome-shell"]; !strings.Contains(g.body, "recipes/") {
		t.Errorf("gnome-shell is a recipe's:\n%s", g.body)
	}
}

const kernelJSON = `{"releases": [
	{"moniker": "mainline", "version": "7.4-rc5", "iseol": false},
	{"moniker": "stable", "version": "7.3.2", "iseol": false},
	{"moniker": "stable", "version": "7.2.9", "iseol": %s},
	{"moniker": "longterm", "version": "6.18.55", "iseol": false},
	{"moniker": "linux-next", "version": "next-20261002", "iseol": false}
]}`

func kernelFindings(t *testing.T, eol, pinned string) []finding {
	t.Helper()
	src := serve(t, map[string]string{"/releases.json": strings.Replace(kernelJSON, "%s", eol, 1)})
	fs, err := src.kernelCheck(pinned).find(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// A point release of the pinned series is routine; a newer series, or the pinned one's end, is
// an issue; a release candidate is neither.
func TestKernelCheck(t *testing.T) {
	if fs := kernelFindings(t, "false", "7.3.1"); len(fs) != 0 {
		t.Errorf("7.3.2 over 7.3.1 (and 7.4-rc5) is routine: %+v", fs)
	}
	fs := kernelFindings(t, "false", "7.2.8")
	if len(fs) != 1 || fs[0].title != "update: kernel 7.2 -> 7.3" || strings.Join(fs[0].labels, ",") != "update,area:kernel" || strings.Contains(fs[0].body, "ended") {
		t.Errorf("7.2 under 7.3: %+v", fs)
	}
	if fs := kernelFindings(t, "true", "7.2.8"); len(fs) != 1 || !strings.Contains(fs[0].body, "ended the 7.2 series") {
		t.Errorf("7.2 at its end: %+v", fs)
	}
	if fs := kernelFindings(t, "false", "7.1.5"); len(fs) != 1 || !strings.Contains(fs[0].body, "ended the 7.1 series") || fs[0].title != "update: kernel 7.1 -> 7.3" {
		t.Errorf("7.1, which kernel.org no longer lists: %+v", fs)
	}
}

func gnomeCache(versions ...string) string {
	files := map[string]map[string]map[string]string{"gnome-shell": {}}
	for _, v := range versions {
		files["gnome-shell"][v] = map[string]string{"tar.xz": v + "/gnome-shell-" + v + ".tar.xz"}
	}
	b, _ := json.Marshal([]any{4, files, map[string][]string{"gnome-shell": versions}, map[string]any{}})
	return string(b)
}

// A new major is an issue; its alphas, betas and release candidates, and point releases of the
// built major, are not.
func TestGnomeCheck(t *testing.T) {
	recipes := t.TempDir()
	if err := os.WriteFile(filepath.Join(recipes, "gnome-shell.yaml"), []byte("package:\n  name: gnome-shell\n  version: \"51.0\"\n  epoch: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name     string
		versions []string
		want     string
	}{
		{"point releases and the next major's previews", []string{"50.5", "51.0", "51.1", "51.2", "52.alpha", "52.beta", "52.rc", "52.rc.1"}, ""},
		{"a new major", []string{"51.2", "52.alpha", "52.0", "52.1"}, "update: GNOME 51 -> 52"},
	} {
		src := serve(t, map[string]string{"/cache.json": gnomeCache(c.versions...)})
		fs, err := src.gnomeCheck(recipes).find(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case c.want == "" && len(fs) != 0:
			t.Errorf("%s: %+v", c.name, fs)
		case c.want != "" && (len(fs) != 1 || fs[0].title != c.want || !strings.Contains(fs[0].body, "gnome-shell 52.1")):
			t.Errorf("%s: %+v", c.name, fs)
		}
	}
}

// Every Intel microcode release is a security issue; the pinned one is none.
func TestMicrocodeCheck(t *testing.T) {
	src := serve(t, map[string]string{"/repos/" + microcodeRepo + "/releases/latest": `{"tag_name": "microcode-20261111", "html_url": "https://example/notes"}`})
	if fs, err := src.microcodeCheck("microcode-20261111").find(t.Context()); err != nil || len(fs) != 0 {
		t.Errorf("the pinned release: %+v, %v", fs, err)
	}
	fs, err := src.microcodeCheck("microcode-20260925").find(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].title != "security: Intel microcode microcode-20260925 -> microcode-20261111" || strings.Join(fs[0].labels, ",") != "security,area:hardware" {
		t.Errorf("%+v", fs)
	}
}

// Every label watch puts on an issue is one labels.json makes.
func TestFindingsUseTheLabels(t *testing.T) {
	labels, err := readLabels("../../.github/labels.json")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, l := range labels {
		have[l.Name] = true
	}
	for _, name := range []string{"security", "severity:critical", "severity:high", "area:packages", "area:kernel", "update", "area:desktop", "area:hardware"} {
		if !have[name] {
			t.Errorf("watch labels issues %s, which labels.json lacks", name)
		}
	}
}

func TestReadEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "versions.env")
	if err := os.WriteFile(p, []byte("# the kernel\nKERNEL_VERSION=7.2.8\n\nGRYPE_IMAGE=anchore/grype:latest@sha256:8c=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := readEnv(p)
	if err != nil || env["KERNEL_VERSION"] != "7.2.8" || env["GRYPE_IMAGE"] != "anchore/grype:latest@sha256:8c=" {
		t.Errorf("%v, %v", env, err)
	}
}
