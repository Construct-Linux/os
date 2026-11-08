package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brandAt is a fake GitHub: brand's files by commit.
func brandAt(files map[string]map[string]string) fetcher {
	return func(_ context.Context, repo, commit, path string) ([]byte, error) {
		if repo != "brand" {
			return nil, os.ErrNotExist
		}
		b, ok := files[commit][path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(b), nil
	}
}

const (
	pinned = "1111111111111111111111111111111111111111"
	older  = "2222222222222222222222222222222222222222"
)

var brandFiles = brandAt(map[string]map[string]string{
	pinned: {"palette/construct.scss": "$construct-light: #00E5FF;\n", "palette/construct.json": `{"cursor-fill": "#0B1116"}`},
	older:  {"palette/construct.scss": "$construct-light: #00FFFF;\n", "palette/construct.json": `{"cursor-fill": "#000000"}`},
})

func fork(t *testing.T, repo string) string {
	t.Helper()
	dir := t.TempDir()
	for _, c := range brandCopies[repo] {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, c.to)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// What fork brand writes is what fork check takes, for each fork that copies brand.
func TestWrittenCopiesCheck(t *testing.T) {
	ctx := context.Background()
	for repo := range brandCopies {
		dir := fork(t, repo)
		if err := writeBrand(ctx, brandFiles, dir, repo, pinned); err != nil {
			t.Fatal(err)
		}
		problems, err := checkBrand(ctx, brandFiles, dir, repo, pinned)
		if err != nil || len(problems) > 0 {
			t.Errorf("%s: %v %v", repo, problems, err)
		}
	}
}

// Copies of another brand commit than the image's fail, and say so by brand.ref.
func TestCopiesOfAnotherBrandFail(t *testing.T) {
	ctx := context.Background()
	dir := fork(t, "Orchis-theme")
	if err := writeBrand(ctx, brandFiles, dir, "Orchis-theme", older); err != nil {
		t.Fatal(err)
	}
	problems, err := checkBrand(ctx, brandFiles, dir, "Orchis-theme", pinned)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 || !strings.Contains(problems[0], "brand.ref") {
		t.Fatalf("problems = %q, want brand.ref's and the copy's", problems)
	}
}

// A copy edited by hand fails, though brand.ref is the image's.
func TestEditedCopyFails(t *testing.T) {
	ctx := context.Background()
	dir := fork(t, "Bibata_Cursor")
	if err := writeBrand(ctx, brandFiles, dir, "Bibata_Cursor", pinned); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "construct-palette.json"), []byte(`{"cursor-fill": "#FF0000"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, err := checkBrand(ctx, brandFiles, dir, "Bibata_Cursor", pinned)
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "construct-palette.json") {
		t.Fatalf("problems = %q, err %v", problems, err)
	}
}

// The SCSS copy says where it came from; the JSON one is brand's bytes.
func TestCopiedHeader(t *testing.T) {
	scss := string(copied(brandCopies["Orchis-theme"][0], pinned, []byte("x\n")))
	if !strings.HasPrefix(scss, "// Copied from Construct-Linux/brand") || !strings.HasSuffix(scss, "\nx\n") {
		t.Errorf("scss copy = %q", scss)
	}
	if got := string(copied(brandCopies["Bibata_Cursor"][0], pinned, []byte("{}"))); got != "{}" {
		t.Errorf("json copy = %q", got)
	}
}
