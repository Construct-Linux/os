package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The recipes' sources before a builder is launched: every git-checkout's branch is on its
// remote, and every tag is, at the commit the recipe expects. A branch merged and deleted, or a
// tag moved, fails melange's clone minutes into a release, buried among the builds beside it;
// git ls-remote says so in seconds, before anything is built.

// checkoutRef is one git-checkout of a recipe, its ${{...}} substituted.
type checkoutRef struct {
	Recipe, Repository, Branch, Tag, Commit string
}

func (c checkoutRef) String() string {
	ref := "branch " + c.Branch
	if c.Tag != "" {
		ref = "tag " + c.Tag
	}
	return fmt.Sprintf("%s: %s %s", c.Recipe, c.Repository, ref)
}

var substitution = regexp.MustCompile(`\$\{\{([a-z]+)\.([A-Za-z0-9_-]+)\}\}`)

// readCheckouts reads a recipe's git-checkouts, line by line as readRecipe does: the package's
// name and version, its vars and var-transforms, and each git-checkout's with block.
func readCheckouts(file string) ([]checkoutRef, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	vals := map[string]string{}
	var transforms []map[string]string
	var checkouts []map[string]string
	var top string
	var cur map[string]string // the git-checkout or var-transform being read
	curIndent := 0
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			top, cur = strings.TrimSuffix(trimmed, ":"), nil
			continue
		}
		item := strings.HasPrefix(trimmed, "- ")
		key, value, isKey := strings.Cut(strings.TrimPrefix(trimmed, "- "), ":")
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if cur != nil && (indent < curIndent || (indent == curIndent-2 && item)) {
			cur = nil
		}
		switch {
		case top == "package" && indent == 2 && isKey && (key == "name" || key == "version"):
			if _, ok := vals["package."+key]; !ok {
				vals["package."+key] = value
			}
		case top == "vars" && indent == 2 && isKey:
			vals["vars."+key] = value
		case top == "var-transforms" && item:
			cur, curIndent = map[string]string{key: value}, indent+2
			transforms = append(transforms, cur)
		case item && isKey && key == "uses" && value == "git-checkout":
			cur, curIndent = map[string]string{}, indent+2
			checkouts = append(checkouts, cur)
		case cur != nil && isKey && key != "with":
			cur[key] = value
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	subst := func(v string) string {
		return substitution.ReplaceAllStringFunc(v, func(m string) string {
			g := substitution.FindStringSubmatch(m)
			if r, ok := vals[g[1]+"."+g[2]]; ok {
				return r
			}
			return m
		})
	}
	// Each transform may read the ones before it, as melange applies them in order.
	for _, t := range transforms {
		re, err := regexp.Compile(t["match"])
		if err != nil {
			return nil, fmt.Errorf("%s: var-transform %s: %w", file, t["to"], err)
		}
		vals["vars."+t["to"]] = re.ReplaceAllString(subst(t["from"]), t["replace"])
	}
	name := strings.TrimSuffix(filepath.Base(file), ".yaml")
	var refs []checkoutRef
	for _, c := range checkouts {
		r := checkoutRef{Recipe: name, Repository: subst(c["repository"]), Branch: subst(c["branch"]),
			Tag: subst(c["tag"]), Commit: c["expected-commit"]}
		for _, v := range []string{r.Repository, r.Branch, r.Tag} {
			if strings.Contains(v, "${{") {
				return nil, fmt.Errorf("%s: git-checkout: %s is not one this check substitutes", file, v)
			}
		}
		if r.Repository == "" || (r.Branch == "") == (r.Tag == "") {
			return nil, fmt.Errorf("%s: git-checkout: a repository and one of branch or tag", file)
		}
		refs = append(refs, r)
	}
	return refs, nil
}

// checkRef is what is wrong with c on its remote, or "": ls-remote's lines are "<sha>\t<ref>", and
// an annotated tag's commit is its peeled ^{} line.
func checkRef(ctx context.Context, c checkoutRef) string {
	ref := "refs/heads/" + c.Branch
	if c.Tag != "" {
		ref = "refs/tags/" + c.Tag
	}
	cmd := exec.CommandContext(ctx, "git", "ls-remote", c.Repository, ref, ref+"^{}")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return "git ls-remote: " + msg
	}
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if sha, name, ok := strings.Cut(l, "\t"); ok {
			got[name] = sha
		}
	}
	sha, ok := got[ref+"^{}"]
	if !ok {
		sha, ok = got[ref]
	}
	switch {
	case !ok && c.Tag != "":
		return "no such tag on the remote"
	case !ok:
		return "no such branch on the remote (merged and deleted? the recipe should name the branch it was merged into)"
	case c.Tag != "" && c.Commit != "" && sha != c.Commit:
		return fmt.Sprintf("the tag is at %s, the recipe expects %s", sha, c.Commit)
	}
	return ""
}

// packagesSources checks every recipe's git-checkouts against their remotes, a few at once, and
// fails with each one that would fail melange's clone.
func packagesSources(recipesDir string) error {
	paths, err := filepath.Glob(filepath.Join(recipesDir, "*.yaml"))
	if err != nil {
		return err
	}
	var refs []checkoutRef
	for _, p := range paths {
		r, err := readCheckouts(p)
		if err != nil {
			return err
		}
		refs = append(refs, r...)
	}
	var (
		mu       sync.Mutex
		failures []string
		wg       sync.WaitGroup
		sem      = make(chan struct{}, 16)
	)
	for _, c := range refs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if msg := checkRef(ctx, c); msg != "" {
				mu.Lock()
				failures = append(failures, fmt.Sprintf("%s: %s", c, msg))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(failures) > 0 {
		sort.Strings(failures)
		for _, f := range failures {
			fmt.Fprintln(os.Stderr, "sources:", f)
		}
		return fmt.Errorf("%d of %d git-checkouts would fail melange's clone", len(failures), len(refs))
	}
	fmt.Printf("sources: %d git-checkouts on their remotes\n", len(refs))
	return nil
}
