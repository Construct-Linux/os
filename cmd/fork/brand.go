package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/forks"
)

// brandCopy is a file a fork holds a copy of from Construct-Linux/brand rather than reading it at
// build: palette.py's outputs, which brand checks (contrast, perception) before it writes them.
type brandCopy struct {
	from, to string
	// comment is how a line comment starts in the copy, for its provenance header; "" for a
	// format with none (JSON), whose provenance is brand.ref's alone.
	comment string
}

// brandCopies are the forks that copy brand's files, by repository. Every copy comes from the
// one brand commit recipes/construct-brand.yaml pins - the commit the image's palette.json,
// Ghostty's and VS Code's themes come from - so the theme, the cursor and the rest of the image
// cannot hold two palettes. The fork records that commit in brand.ref.
var brandCopies = map[string][]brandCopy{
	"Orchis-theme":  {{from: "palette/construct.scss", to: "src/_sass/_construct-palette.scss", comment: "//"}},
	"Bibata_Cursor": {{from: "palette/construct.json", to: "construct-palette.json"}},
}

const brandRef = "brand.ref"

// fetcher reads a file of a repository under Construct-Linux at a commit.
type fetcher func(ctx context.Context, repo, commit, path string) ([]byte, error)

// rawGitHub reads it from raw.githubusercontent.com: brand is public, and a commit's file there
// is that commit's, whatever has been pushed since.
func rawGitHub(ctx context.Context, repo, commit, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	url := fmt.Sprintf("https://raw.githubusercontent.com/Construct-Linux/%s/%s/%s", repo, commit, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// brandPin is the brand commit recipes/construct-brand.yaml builds.
func brandPin(recipes string) (string, error) {
	pin, err := forks.Find(recipes, "construct-brand")
	if err != nil {
		return "", err
	}
	if pin.Name != "brand" {
		return "", fmt.Errorf("%s checks out %s, not brand", pin.Recipe, pin.Name)
	}
	return pin.Commit, nil
}

// copied is a copy's bytes as the fork holds them: brand's file under a header naming where it
// came from, where the format has comments.
func copied(c brandCopy, commit string, raw []byte) []byte {
	if c.comment == "" {
		return raw
	}
	header := fmt.Sprintf("%s Copied from Construct-Linux/brand at brand.ref's commit (%s): its %s.\n"+
		"%s Change the colours in brand's palette.toml, pin brand in os, then task fork:brand.\n",
		c.comment, commit[:12], c.from, c.comment)
	return append([]byte(header), raw...)
}

// writeBrand writes the fork's copies from brand at commit, and brand.ref.
func writeBrand(ctx context.Context, fetch fetcher, dir, repo, commit string) error {
	for _, c := range brandCopies[repo] {
		raw, err := fetch(ctx, "brand", commit, c.from)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, c.to), copied(c, commit, raw), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, brandRef), []byte(commit+"\n"), 0o644)
}

// checkBrand is what is wrong with the fork's copies: brand.ref other than the image's brand
// commit, or a copy other than brand's file at it.
func checkBrand(ctx context.Context, fetch fetcher, dir, repo, commit string) ([]string, error) {
	cs := brandCopies[repo]
	if len(cs) == 0 {
		return nil, nil
	}
	var problems []string
	ref, err := os.ReadFile(filepath.Join(dir, brandRef))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return []string{brandRef + " missing: task fork:brand writes it with the copies"}, nil
	case err != nil:
		return nil, err
	}
	if got := strings.TrimSpace(string(ref)); got != commit {
		problems = append(problems, fmt.Sprintf("%s is %.12s, the image's brand (recipes/construct-brand.yaml) is %.12s: task fork:brand", brandRef, got, commit))
	}
	for _, c := range cs {
		raw, err := fetch(ctx, "brand", commit, c.from)
		if err != nil {
			return nil, err
		}
		have, err := os.ReadFile(filepath.Join(dir, c.to))
		if err != nil {
			return nil, err
		}
		if string(have) != string(copied(c, commit, raw)) {
			problems = append(problems, fmt.Sprintf("%s is not brand's %s at %.12s: task fork:brand", c.to, c.from, commit))
		}
	}
	return problems, nil
}

// brandCmd (fork brand <recipe>) writes the fork's copies of brand's files at the commit the
// image pins brand to.
func brandCmd(ctx context.Context, args []string, out io.Writer) error {
	fl := flag.NewFlagSet("brand", flag.ContinueOnError)
	var w where
	w.flags(fl)
	name, err := parse(fl, args)
	if err != nil {
		return err
	}
	pin, err := forks.Find(w.recipes, name)
	if err != nil {
		return err
	}
	if len(brandCopies[pin.Name]) == 0 {
		return fmt.Errorf("%s holds no copy of brand's files", pin.Name)
	}
	commit, err := brandPin(w.recipes)
	if err != nil {
		return err
	}
	dir := filepath.Join(w.forks, pin.Name)
	if err := writeBrand(ctx, rawGitHub, dir, pin.Name, commit); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: brand's files at %.12s, and %s\n", dir, commit, brandRef)
	return nil
}
