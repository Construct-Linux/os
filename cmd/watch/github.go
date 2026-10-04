package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Label is one of the repository's issue labels.
type Label struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

// Issue is what watch reads and writes of an issue.
type Issue struct {
	Number int
	Title  string
	Body   string
	Labels []string
	Open   bool
	// NotPlanned is an issue a person closed as not planned: watch does not open its finding again.
	NotPlanned bool
}

// GitHub is the repository's labels and issues, as watch reads and writes them.
type GitHub interface {
	Labels(ctx context.Context) ([]Label, error)
	CreateLabel(ctx context.Context, l Label) error
	UpdateLabel(ctx context.Context, name string, l Label) error
	// Issues are every issue, open or closed, pull requests left out.
	Issues(ctx context.Context) ([]Issue, error)
	CreateIssue(ctx context.Context, title, body string, labels []string) (int, error)
	UpdateIssue(ctx context.Context, number int, title, body string, labels []string) error
	CloseIssue(ctx context.Context, number int, comment string) error
}

// rest is GitHub's REST API for one repository.
type rest struct {
	base   string // https://api.github.com
	repo   string // owner/name
	token  string
	client *http.Client
}

// fromEnv is the repository GITHUB_REPOSITORY names, with GITHUB_TOKEN: what a workflow's job has.
func fromEnv() (*rest, error) {
	repo, token := os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_TOKEN")
	if repo == "" || token == "" {
		return nil, errors.New("GITHUB_REPOSITORY (owner/name) and GITHUB_TOKEN must be set")
	}
	return &rest{base: "https://api.github.com", repo: repo, token: token, client: &http.Client{Timeout: 30 * time.Second}}, nil
}

// do sends a request to path, under the repository, with in as its JSON body when not nil, and
// decodes the answer into out when not nil.
func (r *rest) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+"/repos/"+r.repo+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+r.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }() // read to its end or given up on
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// pages is every item of a listing at path, a page of 100 at a time until a short one.
func pages[T any](ctx context.Context, r *rest, path string) ([]T, error) {
	var all []T
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; ; page++ {
		var items []T
		if err := r.do(ctx, http.MethodGet, fmt.Sprintf("%s%sper_page=100&page=%d", path, sep, page), nil, &items); err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < 100 {
			return all, nil
		}
	}
}

func (r *rest) Labels(ctx context.Context) ([]Label, error) {
	return pages[Label](ctx, r, "/labels")
}

func (r *rest) CreateLabel(ctx context.Context, l Label) error {
	return r.do(ctx, http.MethodPost, "/labels", l, nil)
}

func (r *rest) UpdateLabel(ctx context.Context, name string, l Label) error {
	return r.do(ctx, http.MethodPatch, "/labels/"+url.PathEscape(name),
		map[string]string{"new_name": l.Name, "color": l.Color, "description": l.Description}, nil)
}

func (r *rest) Issues(ctx context.Context) ([]Issue, error) {
	type issue struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		Body        string `json:"body"`
		State       string `json:"state"`
		StateReason string `json:"state_reason"`
		Labels      []struct {
			Name string `json:"name"`
		} `json:"labels"`
		PullRequest *struct{} `json:"pull_request"`
	}
	raw, err := pages[issue](ctx, r, "/issues?state=all")
	if err != nil {
		return nil, err
	}
	var issues []Issue
	for _, i := range raw {
		if i.PullRequest != nil {
			continue
		}
		is := Issue{Number: i.Number, Title: i.Title, Body: i.Body, Open: i.State == "open", NotPlanned: i.StateReason == "not_planned"}
		for _, l := range i.Labels {
			is.Labels = append(is.Labels, l.Name)
		}
		issues = append(issues, is)
	}
	return issues, nil
}

func (r *rest) CreateIssue(ctx context.Context, title, body string, labels []string) (int, error) {
	var created struct {
		Number int `json:"number"`
	}
	err := r.do(ctx, http.MethodPost, "/issues", map[string]any{"title": title, "body": body, "labels": labels}, &created)
	return created.Number, err
}

func (r *rest) UpdateIssue(ctx context.Context, number int, title, body string, labels []string) error {
	return r.do(ctx, http.MethodPatch, fmt.Sprintf("/issues/%d", number),
		map[string]any{"title": title, "body": body, "labels": labels}, nil)
}

func (r *rest) CloseIssue(ctx context.Context, number int, comment string) error {
	if err := r.do(ctx, http.MethodPost, fmt.Sprintf("/issues/%d/comments", number), map[string]string{"body": comment}, nil); err != nil {
		return err
	}
	return r.do(ctx, http.MethodPatch, fmt.Sprintf("/issues/%d", number),
		map[string]string{"state": "closed", "state_reason": "completed"}, nil)
}

// logged says each write as it makes it, or with dry says it instead of making it. Reads go to
// the repository when there is one to read - so a dry run says update where a run would - and
// find nothing when there is none.
type logged struct {
	gh  GitHub // nil: no repository to read
	out io.Writer
	dry bool
}

func (l logged) say(format string, args ...any) {
	if l.dry {
		format = "would " + format
	}
	fmt.Fprintf(l.out, format+"\n", args...)
}

func (l logged) Labels(ctx context.Context) ([]Label, error) {
	if l.gh == nil {
		return nil, nil
	}
	return l.gh.Labels(ctx)
}

func (l logged) CreateLabel(ctx context.Context, lb Label) error {
	l.say("create label %s (#%s): %s", lb.Name, lb.Color, lb.Description)
	if l.dry {
		return nil
	}
	return l.gh.CreateLabel(ctx, lb)
}

func (l logged) UpdateLabel(ctx context.Context, name string, lb Label) error {
	l.say("update label %s to %s (#%s): %s", name, lb.Name, lb.Color, lb.Description)
	if l.dry {
		return nil
	}
	return l.gh.UpdateLabel(ctx, name, lb)
}

func (l logged) Issues(ctx context.Context) ([]Issue, error) {
	if l.gh == nil {
		return nil, nil
	}
	return l.gh.Issues(ctx)
}

func (l logged) CreateIssue(ctx context.Context, title, body string, labels []string) (int, error) {
	l.say("open %q [%s]\n%s", title, strings.Join(labels, ", "), indent(body))
	if l.dry {
		return 0, nil
	}
	return l.gh.CreateIssue(ctx, title, body, labels)
}

func (l logged) UpdateIssue(ctx context.Context, number int, title, body string, labels []string) error {
	l.say("update #%d to %q [%s]\n%s", number, title, strings.Join(labels, ", "), indent(body))
	if l.dry {
		return nil
	}
	return l.gh.UpdateIssue(ctx, number, title, body, labels)
}

func (l logged) CloseIssue(ctx context.Context, number int, comment string) error {
	l.say("close #%d: %s", number, comment)
	if l.dry {
		return nil
	}
	return l.gh.CloseIssue(ctx, number, comment)
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}

// repository is the repository the environment names, every write said on out; with dry nothing
// is written, and the environment need not name one.
func repository(dry bool, out io.Writer) (GitHub, error) {
	r, err := fromEnv()
	switch {
	case err == nil:
		return logged{gh: r, out: out, dry: dry}, nil
	case dry:
		fmt.Fprintf(out, "a dry run with no repository to read (%v): everything reads as new\n", err)
		return logged{out: out, dry: true}, nil
	}
	return nil, err
}
