package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// finding is something worth an issue: a security fix, or a big upstream change. Routine updates
// are never findings - Wolfi's rebuilds and point releases are what a release picks up anyway.
type finding struct {
	// key names the finding across runs: the issue carries it as a hidden marker, so a later run
	// finds the issue again. Its first part is the check's: security:openssl, kernel.
	key    string
	title  string
	labels []string
	body   string // without the marker
}

func marker(key string) string { return "<!-- watch:" + key + " -->" }

// markerRE takes any key a marker was written with: the SBOM names Go modules and npm packages
// too (golang.org/x/net, @scope/pkg), and a key it could not read back opened an issue every run.
var markerRE = regexp.MustCompile(`<!-- watch:(\S+) -->`)

// check is one source of findings: every key it finds starts with scope, and an open issue in its
// scope it no longer finds was resolved, which gone says.
type check struct {
	scope string
	gone  string
	find  func(context.Context) ([]finding, error)
}

func (c check) owns(key string) bool {
	return key == c.scope || strings.HasPrefix(key, c.scope+":")
}

// reconcile runs each check and brings the repository's issues to what they found: an issue
// opened for each new finding, the open one updated for each known one, and the open issues of a
// check that no longer finds them closed. A check that fails touches none of its issues; the
// others go on, and the error is answered at the end. A finding a person closed as not planned
// is not opened again.
func reconcile(ctx context.Context, gh GitHub, checks []check, out io.Writer) error {
	issues, err := gh.Issues(ctx)
	if err != nil {
		return err
	}
	open, closed := map[string]Issue{}, map[string]Issue{}
	for _, i := range issues {
		m := markerRE.FindStringSubmatch(i.Body)
		if m == nil {
			continue
		}
		key := m[1]
		if i.Open {
			// Two open issues of one key are a person's doing; the older is the one kept current.
			if cur, ok := open[key]; !ok || i.Number < cur.Number {
				open[key] = i
			}
		} else if cur, ok := closed[key]; !ok || i.Number > cur.Number {
			closed[key] = i
		}
	}
	var errs []error
	for _, c := range checks {
		found, err := c.find(ctx)
		if err != nil {
			fmt.Fprintf(out, "%s: %v: its issues are left as they are\n", c.scope, err)
			errs = append(errs, fmt.Errorf("%s: %w", c.scope, err))
			continue
		}
		fmt.Fprintf(out, "%s: %d finding(s)\n", c.scope, len(found))
		seen := map[string]bool{}
		for _, f := range found {
			seen[f.key] = true
			if err := file(ctx, gh, f, open, closed, out); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", f.key, err))
			}
		}
		for key, i := range open {
			if !c.owns(key) || seen[key] {
				continue
			}
			if err := gh.CloseIssue(ctx, i.Number, c.gone); err != nil {
				errs = append(errs, fmt.Errorf("%s: closing #%d: %w", key, i.Number, err))
			}
		}
	}
	return errors.Join(errs...)
}

// file opens f's issue, or updates the open one when it says something else.
func file(ctx context.Context, gh GitHub, f finding, open, closed map[string]Issue, out io.Writer) error {
	body := strings.TrimRight(f.body, "\n") + "\n\n" + marker(f.key) + "\n"
	if i, ok := open[f.key]; ok {
		// The labels a person added stay.
		labels := slices.Clone(i.Labels)
		for _, l := range f.labels {
			if !slices.Contains(labels, l) {
				labels = append(labels, l)
			}
		}
		if i.Title == f.title && i.Body == body && len(labels) == len(i.Labels) {
			fmt.Fprintf(out, "  #%d %s: as it is\n", i.Number, f.title)
			return nil
		}
		return gh.UpdateIssue(ctx, i.Number, f.title, body, labels)
	}
	if i, ok := closed[f.key]; ok && i.NotPlanned {
		fmt.Fprintf(out, "  #%d %s: closed as not planned, not opened again\n", i.Number, f.title)
		return nil
	}
	_, err := gh.CreateIssue(ctx, f.title, body, f.labels)
	return err
}

func runCmd(ctx context.Context, args []string, out io.Writer) error {
	fl := flag.NewFlagSet("run", flag.ContinueOnError)
	grypeJSON := fl.String("grype", "", "Grype's JSON report of the latest release's SBOM; without it security issues are left as they are")
	release := fl.String("release", "", "the release the Grype report is of, which the issues name")
	versions := fl.String("versions", "build/versions.env", "the pins")
	recipes := fl.String("recipes", "recipes", "the recipes' directory")
	dry := fl.Bool("dry-run", false, "print the findings and what would be done, change nothing")
	if err := fl.Parse(args); err != nil {
		return err
	}
	pins, err := readEnv(*versions)
	if err != nil {
		return err
	}
	gh, err := repository(*dry, out)
	if err != nil {
		return err
	}
	src := sources{client: &http.Client{Timeout: time.Minute}, kernel: kernelReleases, gnome: gnomeShellCache, github: githubAPI}
	checks := []check{
		src.kernelCheck(pins["KERNEL_VERSION"]),
		src.gnomeCheck(*recipes),
		src.microcodeCheck(pins["INTEL_MICROCODE_VERSION"]),
	}
	if *grypeJSON != "" {
		checks = append([]check{securityCheck(*grypeJSON, *release, *recipes)}, checks...)
	} else {
		fmt.Fprintln(out, "security: no Grype report given: its issues are left as they are")
	}
	return reconcile(ctx, gh, checks, out)
}
