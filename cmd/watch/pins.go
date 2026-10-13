package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/Construct-Linux/os/internal/update"
)

// Where the pins Dependabot cannot read are released: Go's downloads, and GitLab's API for the
// projects that publish there.
const (
	goReleases = "https://go.dev/dl/?mode=json"
	gitlabAPI  = "https://gitlab.com/api/v4"
)

// pin is a version build/versions.env or build/host.env pins, and where upstream says its newest.
// The image digests there (wolfi-base, grype, syft, debian) are left out: a :latest digest moves
// most days, and an issue for it would never close.
type pin struct {
	env, file, name string
	newest          func(context.Context, sources) (string, error)
}

var pins = []pin{
	{"GO_VERSION", "build/host.env", "Go", newestGo},
	{"TASK_VERSION", "build/host.env", "task", githubLatest("go-task/task")},
	{"GH_VERSION", "build/host.env", "gh", githubLatest("cli/cli")},
	{"LINUX_FIRMWARE_VERSION", "build/versions.env", "linux-firmware", gitlabNewest("kernel-firmware/linux-firmware", `^[0-9]{8}$`)},
	{"QEMU_VERSION", "build/versions.env", "QEMU (the harness's)", gitlabNewest("qemu-project/qemu", `^v[0-9]+\.[0-9]+\.[0-9]+$`)},
}

// newestGo is go.dev's newest stable Go, without its go prefix.
func newestGo(ctx context.Context, s sources) (string, error) {
	var rels []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := s.getJSON(ctx, s.goDL, &rels); err != nil {
		return "", err
	}
	newest := ""
	for _, r := range rels {
		if v := strings.TrimPrefix(r.Version, "go"); r.Stable && (newest == "" || update.Compare(v, newest) > 0) {
			newest = v
		}
	}
	return newest, nil
}

// githubLatest is a GitHub repository's latest release, without its v prefix.
func githubLatest(repo string) func(context.Context, sources) (string, error) {
	return func(ctx context.Context, s sources) (string, error) {
		var rel struct {
			Tag string `json:"tag_name"`
		}
		if err := s.getJSON(ctx, s.github+"/repos/"+repo+"/releases/latest", &rel); err != nil {
			return "", err
		}
		return strings.TrimPrefix(rel.Tag, "v"), nil
	}
}

// gitlabNewest is the newest of a GitLab project's tags that release matches - no release
// candidates - without a v prefix.
func gitlabNewest(project, release string) func(context.Context, sources) (string, error) {
	re := regexp.MustCompile(release)
	return func(ctx context.Context, s sources) (string, error) {
		var tags []struct {
			Name string `json:"name"`
		}
		u := s.gitlab + "/projects/" + url.PathEscape(project) + "/repository/tags?order_by=version&sort=desc&per_page=100"
		if err := s.getJSON(ctx, u, &tags); err != nil {
			return "", err
		}
		newest := ""
		for _, t := range tags {
			if v := strings.TrimPrefix(t.Name, "v"); re.MatchString(t.Name) && (newest == "" || update.Compare(v, newest) > 0) {
				newest = v
			}
		}
		return newest, nil
	}
}

// pinsCheck compares each pin in env - versions.env's and host.env's lines - with upstream's
// newest: one issue lists every pin behind, updated as they move, closed when none is.
func (s sources) pinsCheck(env map[string]string) check {
	return check{
		scope: "pins",
		gone:  "build/versions.env and build/host.env pin upstream's newest: closing.",
		find: func(ctx context.Context) ([]finding, error) {
			var b strings.Builder
			behind := 0
			var errs []error
			for _, p := range pins {
				newest, err := p.newest(ctx, s)
				if err == nil && newest == "" {
					err = errors.New("no release")
				}
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", p.name, err))
					continue
				}
				if update.Compare(newest, env[p.env]) <= 0 {
					continue
				}
				behind++
				fmt.Fprintf(&b, "| %s | `%s` in %s | %s | %s |\n", p.name, p.env, p.file, env[p.env], newest)
			}
			// One upstream that did not answer leaves the issue as it is, rather than closing it.
			if err := errors.Join(errs...); err != nil {
				return nil, err
			}
			if behind == 0 {
				return nil, nil
			}
			body := "Pins behind upstream's newest release:\n\n| | pin | pinned | newest |\n|---|---|---|---:|\n" + b.String() +
				"\nBump each with its SHA256 beside it. Go's is go.mod's `go` line too: a test holds them together. " +
				"A new QEMU or linux-firmware is checked by `task test:usb` and `task test:lifecycle`.\n"
			return []finding{{key: "pins", title: fmt.Sprintf("update: %d pin(s) behind upstream", behind),
				labels: []string{"update", "area:build"}, body: body}}, nil
		},
	}
}
