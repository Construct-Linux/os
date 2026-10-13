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

// builtAt is the time a version stands for, which every file of its root is stamped with: a
// release's day and its build number as seconds into it (20261005.2 is 00:00:02 that day), a
// development build's its own time (20261005.142233). The same version is the same time - the same
// tree is the same image - and each release's differs, as the caches that key on it need.
func builtAt(version string) (time.Time, error) {
	// A build's time first: its six digits are a release's form too (YYYYMMDD.N), and as a number
	// of seconds they would be a day and a half.
	if t, err := time.Parse("20060102.150405", version); err == nil {
		return t, nil
	}
	if !calver.MatchString(version) {
		return time.Time{}, fmt.Errorf("version %q is neither a release's (YYYYMMDD.N) nor a build's time", version)
	}
	day, n, _ := strings.Cut(version, ".")
	t, err := time.Parse("20060102", day)
	if err != nil {
		return time.Time{}, fmt.Errorf("version %q: %w", version, err)
	}
	k, err := strconv.Atoi(n)
	if err != nil {
		return time.Time{}, fmt.Errorf("version %q: %w", version, err)
	}
	return t.Add(time.Duration(k) * time.Second), nil
}

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
	// The remote whose tags count.
	remote := "origin"
	_ = fl.Parse(args)
	day := time.Now().UTC()
	out, err := output(exec.Command("git", "ls-remote", "--tags", remote, "refs/tags/v"+day.Format("20060102")+".*"))
	if err != nil {
		return err
	}
	var tags []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			tags = append(tags, f[1])
		}
	}
	fmt.Println(nextVersion(day, tags))
	return nil
}
