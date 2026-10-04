package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// found is a check of scope that finds fs.
func found(scope string, fs ...finding) check {
	return check{scope: scope, gone: scope + " resolved", find: func(context.Context) ([]finding, error) { return fs, nil }}
}

var openssl = finding{key: "security:openssl", title: "security: openssl 3.5.0 - 1 fixable (High)",
	labels: []string{"security", "severity:high", "area:packages"}, body: "one High"}

// A finding with no issue opens one, carrying its marker; the next run finds it by the marker and
// leaves it as it is, whatever else the repository holds.
func TestReconcileOpensOnceAndDedupesByMarker(t *testing.T) {
	gh := &fakeGitHub{issues: []Issue{
		{Number: 1, Title: "a person's bug", Body: "it crashes", Open: true},
		{Number: 2, Title: "openssl is old", Body: "no marker here: a person's issue about openssl", Open: true},
	}}
	for range 2 {
		if err := reconcile(t.Context(), gh, []check{found("security", openssl)}, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if len(gh.issues) != 3 || gh.writes != 1 {
		t.Fatalf("%d issues after %d writes, want 3 after 1", len(gh.issues), gh.writes)
	}
	i := gh.issues[2]
	if !strings.Contains(i.Body, "<!-- watch:security:openssl -->") || i.Title != openssl.title || strings.Join(i.Labels, ",") != "security,severity:high,area:packages" {
		t.Errorf("opened %+v", i)
	}
}

// A finding that changed updates its open issue, keeping the labels a person added.
func TestReconcileUpdates(t *testing.T) {
	gh := &fakeGitHub{issues: []Issue{{Number: 7, Title: "old title", Open: true,
		Body: "old\n\n<!-- watch:security:openssl -->\n", Labels: []string{"security", "agent-ready"}}}}
	if err := reconcile(t.Context(), gh, []check{found("security", openssl)}, io.Discard); err != nil {
		t.Fatal(err)
	}
	i := gh.issues[0]
	if len(gh.issues) != 1 || i.Title != openssl.title || !strings.HasPrefix(i.Body, "one High") {
		t.Fatalf("not updated: %+v", gh.issues)
	}
	if got := strings.Join(i.Labels, ","); got != "security,agent-ready,severity:high,area:packages" {
		t.Errorf("labels %s", got)
	}
}

// An open issue whose finding is gone is closed with its check's reason; another check's issues,
// and those of a check that failed, are left alone.
func TestReconcileClosesWhatIsResolved(t *testing.T) {
	gh := &fakeGitHub{issues: []Issue{
		{Number: 1, Body: "<!-- watch:security:openssl -->", Open: true},
		{Number: 2, Body: "<!-- watch:security:glibc -->", Open: true},
		{Number: 3, Body: "<!-- watch:kernel -->", Open: true},
		{Number: 4, Body: "<!-- watch:gnome -->", Open: true},
	}}
	failing := check{scope: "kernel", find: func(context.Context) ([]finding, error) { return nil, errors.New("kernel.org is down") }}
	err := reconcile(t.Context(), gh, []check{found("security", openssl), failing}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "kernel.org is down") {
		t.Errorf("the failed check's error: %v", err)
	}
	for n, open := range map[int]bool{1: true, 2: false, 3: true, 4: true} {
		if gh.issue(n).Open != open {
			t.Errorf("#%d open = %v, want %v", n, gh.issue(n).Open, open)
		}
	}
	if c := gh.comments[2]; len(c) != 1 || c[0] != "security resolved" {
		t.Errorf("#2's comments: %q", c)
	}
}

// A finding a person closed as not planned stays closed; one watch closed as resolved that comes
// back opens a new issue.
func TestReconcileAfterClose(t *testing.T) {
	gh := &fakeGitHub{issues: []Issue{
		{Number: 1, Body: "<!-- watch:security:openssl -->", NotPlanned: true},
		{Number: 2, Body: "<!-- watch:kernel -->"},
	}}
	kernel := finding{key: "kernel", title: "update: kernel 7.2 -> 7.3", labels: []string{"update", "area:kernel"}, body: "newer"}
	if err := reconcile(t.Context(), gh, []check{found("security", openssl), found("kernel", kernel)}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(gh.issues) != 3 || gh.issues[2].Title != kernel.title || !gh.issues[2].Open {
		t.Errorf("issues %+v", gh.issues)
	}
}

// A dry run says what it would do and writes nothing.
func TestReconcileDryRun(t *testing.T) {
	gh := &fakeGitHub{issues: []Issue{{Number: 1, Body: "<!-- watch:security:glibc -->", Open: true}}}
	var out strings.Builder
	if err := reconcile(t.Context(), logged{gh: gh, out: &out, dry: true}, []check{found("security", openssl)}, &out); err != nil {
		t.Fatal(err)
	}
	if gh.writes != 0 {
		t.Errorf("%d writes", gh.writes)
	}
	for _, want := range []string{`would open "security: openssl`, "would close #1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in\n%s", want, out.String())
		}
	}
}
