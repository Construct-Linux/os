package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub is a repository in memory: its labels and issues, and the comments closes left.
type fakeGitHub struct {
	labels   []Label
	issues   []Issue
	comments map[int][]string
	writes   int
}

func (f *fakeGitHub) Labels(context.Context) ([]Label, error) { return f.labels, nil }

func (f *fakeGitHub) CreateLabel(_ context.Context, l Label) error {
	f.writes++
	f.labels = append(f.labels, l)
	return nil
}

func (f *fakeGitHub) UpdateLabel(_ context.Context, name string, l Label) error {
	f.writes++
	for i := range f.labels {
		if f.labels[i].Name == name {
			f.labels[i] = l
			return nil
		}
	}
	return fmt.Errorf("no label %s", name)
}

func (f *fakeGitHub) Issues(context.Context) ([]Issue, error) { return f.issues, nil }

func (f *fakeGitHub) CreateIssue(_ context.Context, title, body string, labels []string) (int, error) {
	f.writes++
	n := len(f.issues) + 1
	f.issues = append(f.issues, Issue{Number: n, Title: title, Body: body, Labels: labels, Open: true})
	return n, nil
}

func (f *fakeGitHub) issue(n int) *Issue {
	for i := range f.issues {
		if f.issues[i].Number == n {
			return &f.issues[i]
		}
	}
	return nil
}

func (f *fakeGitHub) UpdateIssue(_ context.Context, n int, title, body string, labels []string) error {
	f.writes++
	i := f.issue(n)
	if i == nil {
		return fmt.Errorf("no issue %d", n)
	}
	i.Title, i.Body, i.Labels = title, body, labels
	return nil
}

func (f *fakeGitHub) CloseIssue(_ context.Context, n int, comment string) error {
	f.writes++
	i := f.issue(n)
	if i == nil {
		return fmt.Errorf("no issue %d", n)
	}
	i.Open = false
	if f.comments == nil {
		f.comments = map[int][]string{}
	}
	f.comments[n] = append(f.comments[n], comment)
	return nil
}

// The REST client pages through a listing, leaves pull requests out of the issues, reads a
// person's not-planned close, and escapes a label's colon in its path.
func TestRESTClient(t *testing.T) {
	var patched string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues":
			var page []map[string]any
			if r.URL.Query().Get("page") == "1" {
				for n := 1; n <= 100; n++ {
					page = append(page, map[string]any{"number": n, "state": "open"})
				}
			} else if r.URL.Query().Get("page") == "2" {
				page = []map[string]any{
					{"number": 101, "state": "closed", "state_reason": "not_planned", "labels": []map[string]string{{"name": "security"}}},
					{"number": 102, "state": "open", "pull_request": map[string]string{}},
				}
			}
			_ = json.NewEncoder(w).Encode(page)
		case r.Method == http.MethodPatch:
			patched = r.URL.EscapedPath()
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r := &rest{base: srv.URL, repo: "o/r", token: "tok", client: srv.Client()}
	issues, err := r.Issues(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 101 {
		t.Fatalf("%d issues, want 101 (the pull request left out)", len(issues))
	}
	if last := issues[100]; last.Number != 101 || last.Open || !last.NotPlanned || strings.Join(last.Labels, ",") != "security" {
		t.Errorf("issue 101 read as %+v", last)
	}
	if err := r.UpdateLabel(t.Context(), "area:boot", Label{Name: "area:boot", Color: "c5def5", Description: "x"}); err != nil {
		t.Fatal(err)
	}
	if patched != "/repos/o/r/labels/area:boot" && patched != "/repos/o/r/labels/area%3Aboot" {
		t.Errorf("patched %s", patched)
	}
	if err := (&rest{base: srv.URL, repo: "o/r", token: "bad", client: srv.Client()}).CreateLabel(t.Context(), Label{}); err == nil {
		t.Error("a refused request was no error")
	}
}
