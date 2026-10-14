package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Construct-Linux/os/internal/update"
)

// Where upstream says what it released.
const (
	kernelReleases  = "https://www.kernel.org/releases.json"
	gnomeShellCache = "https://download.gnome.org/sources/gnome-shell/cache.json"
	githubAPI       = "https://api.github.com"
	microcodeRepo   = "intel/Intel-Linux-Processor-Microcode-Data-Files"
)

// sources are the upstream endpoints the checks read: the real ones, or a test's servers.
type sources struct {
	client *http.Client
	// token is GITHUB_TOKEN, for GitHub's API: anonymous calls share a runner's address's 60 an hour.
	token  string
	kernel string
	gnome  string
	github string
	goDL   string
	gitlab string
}

// getJSON decodes the JSON at url into out.
func (s sources) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if strings.HasPrefix(url, s.github+"/") {
		req.Header.Set("Accept", "application/vnd.github+json")
		if s.token != "" {
			req.Header.Set("Authorization", "Bearer "+s.token)
		}
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }() // read to its end or given up on
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out); err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	return nil
}

// readEnv is the KEY=VALUE lines of a dotenv file: build/versions.env.
func readEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			env[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return env, sc.Err()
}

// series is a version's first two numbers, the kernel's series: 7.2 of 7.2.8.
type series struct{ major, minor int }

func parseSeries(v string) (series, bool) {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return series{}, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	return series{major, minor}, err1 == nil && err2 == nil
}

func (s series) String() string { return fmt.Sprintf("%d.%d", s.major, s.minor) }

func (s series) newer(o series) bool {
	return s.major > o.major || (s.major == o.major && s.minor > o.minor)
}

// kernelCheck compares the pinned kernel with kernel.org's: a newer stable or longterm series, or
// the pinned one at its end of life, is an issue; so is a point release of the pinned series, which
// carries the stable team's fixes - security ones among them - and no new features.
func (s sources) kernelCheck(pinned string) check {
	return check{
		scope: "kernel",
		gone:  "build/versions.env pins kernel.org's newest release of a supported series: closing.",
		find: func(ctx context.Context) ([]finding, error) {
			var r struct {
				Releases []struct {
					Moniker string `json:"moniker"`
					Version string `json:"version"`
					EOL     bool   `json:"iseol"`
				} `json:"releases"`
			}
			if err := s.getJSON(ctx, s.kernel, &r); err != nil {
				return nil, err
			}
			pin, ok := parseSeries(pinned)
			if !ok {
				return nil, fmt.Errorf("KERNEL_VERSION %q is no kernel version", pinned)
			}
			// kernel.org lists only the series it maintains: one it no longer lists is at its end.
			eol, listed := false, false
			var newest series
			newestVersion, newestMoniker, point := "", "", ""
			for _, rel := range r.Releases {
				if rel.Moniker != "stable" && rel.Moniker != "longterm" {
					continue
				}
				ser, ok := parseSeries(rel.Version)
				if !ok {
					continue
				}
				if ser == pin {
					listed, eol = true, eol || rel.EOL
					if update.Compare(rel.Version, pinned) > 0 {
						point = rel.Version
					}
				}
				if !rel.EOL && ser.newer(pin) && ser.newer(newest) {
					newest, newestVersion, newestMoniker = ser, rel.Version, rel.Moniker
				}
			}
			eol = eol || !listed
			var found []finding
			if point != "" {
				found = append(found, finding{key: "kernel:point", title: fmt.Sprintf("update: kernel %s -> %s", pinned, point),
					labels: []string{"update", "area:kernel"}, body: fmt.Sprintf("build/versions.env pins the kernel at %s; kernel.org released %s, "+
						"the stable team's fixes to the same series (https://cdn.kernel.org/pub/linux/kernel/v%d.x/ChangeLog-%s).\n\n"+
						"Bump KERNEL_VERSION and KERNEL_SHA256 in build/versions.env, and Arch's base config in kernel/Dockerfile to the same version "+
						"(its tag and checksum); check that kernel/patches/ still apply and the fragments hold (`task kernel:config`).\n", pinned, point, pin.major, point)})
			}
			if newestVersion == "" && !eol {
				return found, nil
			}
			title := fmt.Sprintf("update: kernel %s -> %s", pin, newest)
			var b strings.Builder
			fmt.Fprintf(&b, "build/versions.env pins the kernel at %s.", pinned)
			if eol {
				fmt.Fprintf(&b, " kernel.org has ended the %s series: it gets no more fixes.", pin)
			}
			if newestVersion == "" {
				title = fmt.Sprintf("update: kernel %s is end of life", pin)
			} else {
				fmt.Fprintf(&b, " A newer series is out: %s (%s).", newestVersion, newestMoniker)
			}
			fmt.Fprint(&b, "\n\nBump KERNEL_VERSION and KERNEL_SHA256 in build/versions.env, and Arch's base config in kernel/Dockerfile to the same version; a new series can fail the fragments' assertions (`task kernel:config`).\n")
			return append(found, finding{key: "kernel", title: title, labels: []string{"update", "area:kernel"}, body: b.String()}), nil
		},
	}
}

// recipeVersion is the package's version in a melange recipe.
var recipeVersion = regexp.MustCompile(`(?m)^  version: *"?([0-9][^"\s]*)"?\s*$`)

// gnomeStable is a GNOME release, not an alpha, beta or release candidate (51.alpha, 51.rc).
var gnomeStable = regexp.MustCompile(`^([0-9]+)(\.[0-9]+)+$`)

// gnomeCheck compares the GNOME major recipes/gnome-shell.yaml builds with the newest stable
// gnome-shell: a new major is an issue, a point release of the same major is routine.
func (s sources) gnomeCheck(recipes string) check {
	return check{
		scope: "gnome",
		gone:  "recipes/gnome-shell.yaml builds GNOME's newest major: closing.",
		find: func(ctx context.Context) ([]finding, error) {
			b, err := os.ReadFile(filepath.Join(recipes, "gnome-shell.yaml"))
			if err != nil {
				return nil, err
			}
			m := recipeVersion.FindSubmatch(b)
			if m == nil {
				return nil, errors.New("recipes/gnome-shell.yaml: no version")
			}
			ours, err := strconv.Atoi(strings.SplitN(string(m[1]), ".", 2)[0])
			if err != nil {
				return nil, fmt.Errorf("recipes/gnome-shell.yaml: version %q", m[1])
			}
			// cache.json is [format, {module: {version: files}}, {module: [versions]}, ...].
			var cache []json.RawMessage
			if err := s.getJSON(ctx, s.gnome, &cache); err != nil {
				return nil, err
			}
			var files map[string]map[string]json.RawMessage
			if len(cache) < 2 {
				return nil, errors.New("gnome-shell's cache.json: no releases")
			}
			if err := json.Unmarshal(cache[1], &files); err != nil {
				return nil, fmt.Errorf("gnome-shell's cache.json: %w", err)
			}
			newest, newestVersion := ours, ""
			for v := range files["gnome-shell"] {
				sm := gnomeStable.FindStringSubmatch(v)
				if sm == nil {
					continue
				}
				major, _ := strconv.Atoi(sm[1])
				if major > newest || (major == newest && newestVersion != "" && update.Compare(v, newestVersion) > 0) {
					newest, newestVersion = major, v
				}
			}
			if newestVersion == "" {
				return nil, nil
			}
			body := fmt.Sprintf("recipes/gnome-shell.yaml builds GNOME %d (gnome-shell %s); GNOME %d is out (gnome-shell %s).\n\n"+
				"A new major moves the shell, mutter and their kin together, and Orchis and the extensions with them.\n",
				ours, m[1], newest, newestVersion)
			return []finding{{key: "gnome", title: fmt.Sprintf("update: GNOME %d -> %d", ours, newest),
				labels: []string{"update", "area:desktop"}, body: body}}, nil
		},
	}
}

// microcodeCheck compares the pinned Intel microcode with Intel's latest release. Intel releases
// microcode to fix vulnerabilities, so every new one is a security issue.
func (s sources) microcodeCheck(pinned string) check {
	return check{
		scope: "microcode",
		gone:  "build/versions.env pins Intel's latest microcode: closing.",
		find: func(ctx context.Context) ([]finding, error) {
			var rel struct {
				Tag string `json:"tag_name"`
				URL string `json:"html_url"`
			}
			if err := s.getJSON(ctx, s.github+"/repos/"+microcodeRepo+"/releases/latest", &rel); err != nil {
				return nil, err
			}
			if rel.Tag == "" || update.Compare(rel.Tag, pinned) <= 0 {
				return nil, nil
			}
			body := fmt.Sprintf("build/versions.env pins Intel's microcode at %s; Intel released %s: %s\n\n"+
				"Intel's microcode releases fix vulnerabilities (the release notes name the advisories). Bump INTEL_MICROCODE_VERSION and INTEL_MICROCODE_SHA256.\n",
				pinned, rel.Tag, rel.URL)
			return []finding{{key: "microcode", title: fmt.Sprintf("security: Intel microcode %s -> %s", pinned, rel.Tag),
				labels: []string{"security", "area:hardware"}, body: body}}, nil
		},
	}
}
