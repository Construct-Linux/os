package main

import (
	"testing"
	"time"
)

// A day's first build is .1; the next is one more than the day's highest, other days' tags aside.
func TestNextVersion(t *testing.T) {
	day := time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		tags []string
		want string
	}{
		{nil, "20261004.1"},
		{[]string{"refs/tags/v20261003.7"}, "20261004.1"},
		{[]string{"refs/tags/v20261004.1", "refs/tags/v20261004.9", "refs/tags/v20261004.10^{}"}, "20261004.10"},
		{[]string{"v20261004.2", "v20261004.10"}, "20261004.11"},
	} {
		if got := nextVersion(day, c.tags); got != c.want {
			t.Errorf("%v: %s, want %s", c.tags, got, c.want)
		}
	}
}

// A release's version and a development build's both stand for a time: a release's is its day and
// its number as seconds, so the day's releases differ; anything else is refused.
func TestBuiltAt(t *testing.T) {
	for v, want := range map[string]string{
		"20261005.1":      "2026-10-05T00:00:01Z",
		"20261005.12":     "2026-10-05T00:00:12Z",
		"20261005.142233": "2026-10-05T14:22:33Z",
	} {
		got, err := builtAt(v)
		if err != nil || got.Format(time.RFC3339) != want {
			t.Errorf("%s: %v %v, want %s", v, got, err, want)
		}
	}
	for _, bad := range []string{"", "v20261005.1", "20261005", "2026-10-05"} {
		if _, err := builtAt(bad); err == nil {
			t.Errorf("%q passed", bad)
		}
	}
}
