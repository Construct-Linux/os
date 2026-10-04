package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// labelGroup is one group of .github/labels.json: type, area, state or severity.
type labelGroup struct {
	Group  string  `json:"group"`
	Labels []Label `json:"labels"`
}

var labelColor = regexp.MustCompile(`^[0-9a-f]{6}$`)

// readLabels is every label of the file at path, checked: a color GitHub takes, a description
// within GitHub's 100 characters, and no name twice - GitHub's names ignore case.
func readLabels(path string) ([]Label, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var groups []labelGroup
	if err := json.Unmarshal(b, &groups); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	var labels []Label
	for _, g := range groups {
		for _, l := range g.Labels {
			key := strings.ToLower(l.Name)
			switch {
			case l.Name == "":
				return nil, fmt.Errorf("%s: a label of %s has no name", path, g.Group)
			case seen[key]:
				return nil, fmt.Errorf("%s: %s is there twice", path, l.Name)
			case !labelColor.MatchString(l.Color):
				return nil, fmt.Errorf("%s: %s's color %q is not six lowercase hex digits", path, l.Name, l.Color)
			case l.Description == "" || len([]rune(l.Description)) > 100:
				return nil, fmt.Errorf("%s: %s's description must say something in at most 100 characters", path, l.Name)
			}
			seen[key] = true
			labels = append(labels, l)
		}
	}
	if len(labels) == 0 {
		return nil, errors.New(path + " holds no labels")
	}
	return labels, nil
}

// applyLabels creates each of want the repository lacks and updates each that differs. A label
// the repository has and want does not stays: a person may have made it, and issues carry it.
func applyLabels(ctx context.Context, gh GitHub, want []Label) error {
	have, err := gh.Labels(ctx)
	if err != nil {
		return err
	}
	byName := map[string]Label{}
	for _, l := range have {
		byName[strings.ToLower(l.Name)] = l
	}
	for _, l := range want {
		cur, ok := byName[strings.ToLower(l.Name)]
		switch {
		case !ok:
			if err := gh.CreateLabel(ctx, l); err != nil {
				return fmt.Errorf("label %s: %w", l.Name, err)
			}
		case cur.Name != l.Name || !strings.EqualFold(cur.Color, l.Color) || cur.Description != l.Description:
			if err := gh.UpdateLabel(ctx, cur.Name, l); err != nil {
				return fmt.Errorf("label %s: %w", l.Name, err)
			}
		}
	}
	return nil
}

func labelsCmd(ctx context.Context, args []string, out io.Writer) error {
	fl := flag.NewFlagSet("labels", flag.ContinueOnError)
	file := fl.String("file", ".github/labels.json", "the labels, by group")
	dry := fl.Bool("dry-run", false, "print what would change, change nothing")
	if err := fl.Parse(args); err != nil {
		return err
	}
	want, err := readLabels(*file)
	if err != nil {
		return err
	}
	gh, err := repository(*dry, out)
	if err != nil {
		return err
	}
	return applyLabels(ctx, gh, want)
}
