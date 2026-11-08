package main

import (
	"path/filepath"
	"regexp"
	"testing"
)

// Every recipe's git-checkouts read to a repository and a concrete ref, the var-transforms
// applied: a recipe this check cannot read would otherwise be a release's surprise again.
func TestReadCheckoutsOfEveryRecipe(t *testing.T) {
	paths, err := filepath.Glob("../../recipes/*.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("recipes: %v", err)
	}
	byRecipe := map[string][]checkoutRef{}
	for _, p := range paths {
		refs, err := readCheckouts(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range refs {
			byRecipe[r.Recipe] = append(byRecipe[r.Recipe], r)
		}
	}
	// openssh's tag is a var-transform of its version; mesa checks out a tag and a branch.
	if got := byRecipe["openssh"]; len(got) != 1 || !regexp.MustCompile(`^V_\d+_\d+_P\d+$`).MatchString(got[0].Tag) {
		t.Errorf("openssh: %+v, want a tag V_<major>_<minor>_P<patch>", got)
	}
	if got := byRecipe["mesa"]; len(got) < 2 {
		t.Errorf("mesa: %+v, want its translator's tag and the headers' branch", got)
	}
}
