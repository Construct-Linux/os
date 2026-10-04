package main

import (
	"flag"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A release's version is its date and the day's build number, YYYYMMDD.N, tagged vYYYYMMDD.N:
// systemd-sysupdate compares versions segment by segment, numerically, so the 10th build of a day
// is newer than its 9th, and every day's newer than the last's. File names carry the version
// without the v (internal/update Names).

// calver is a release's version.
var calver = regexp.MustCompile(`^[0-9]{8}\.[1-9][0-9]*$`)

// nextVersion is the version after the tags already published on day: the day's first build is
// .1, and a day's next one more than its highest, whatever was skipped between.
func nextVersion(day time.Time, tags []string) string {
	date := day.UTC().Format("20060102")
	n := 0
	for _, t := range tags {
		t = strings.TrimPrefix(strings.TrimPrefix(t, "refs/tags/"), "v")
		rest, ok := strings.CutPrefix(t, date+".")
		if !ok {
			continue
		}
		if k, err := strconv.Atoi(rest); err == nil && k > n {
			n = k
		}
	}
	return fmt.Sprintf("%s.%d", date, n+1)
}

// releaseVersion prints the next version, from the tags the remote has: the builder asks it once,
// before it builds, and tags what it built with it.
func releaseVersion(args []string) error {
	fl := flag.NewFlagSet("release-version", flag.ExitOnError)
	remote := fl.String("remote", "origin", "the remote whose tags count")
	_ = fl.Parse(args)
	day := time.Now().UTC()
	out, err := exec.Command("git", "ls-remote", "--tags", *remote, "refs/tags/v"+day.Format("20060102")+".*").Output()
	if err != nil {
		return fmt.Errorf("git ls-remote: %w", err)
	}
	var tags []string
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			tags = append(tags, f[1])
		}
	}
	fmt.Println(nextVersion(day, tags))
	return nil
}
