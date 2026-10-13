// Command apkorder writes apko's order of the versions on standard input, one a line: the versions
// apko refuses, then the rest from oldest to newest, a line each, versions apko holds equal on one
// line. cmd/image's apkCompare is held to it (TestAPKCompareMatchesApko, task check). A module of
// its own, so apko's dependencies stay out of the repository's binaries; its apko is the one
// Taskfile.yml's APKO image is (apko version), moved with it.
package main

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strings"

	"chainguard.dev/apko/pkg/apk/apk"
)

func main() {
	type version struct {
		s string
		v apk.Version
	}
	var ok []version
	var refused []string
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if v, err := apk.ParseVersion(s); err != nil {
			refused = append(refused, s)
		} else {
			ok = append(ok, version{s, v})
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	slices.Sort(refused)
	for _, s := range refused {
		fmt.Println("refused", s)
	}
	slices.SortStableFunc(ok, func(a, b version) int {
		if c := apk.CompareVersions(a.v, b.v); c != 0 {
			return c
		}
		return strings.Compare(a.s, b.s)
	})
	for i, v := range ok {
		switch {
		case i == 0:
			fmt.Print(v.s)
		case apk.CompareVersions(ok[i-1].v, v.v) == 0:
			fmt.Print(" ", v.s)
		default:
			fmt.Print("\n", v.s)
		}
	}
	fmt.Println()
}
