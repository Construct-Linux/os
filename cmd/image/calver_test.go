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
