package update

import "testing"

// Versions are ordered as systemd orders them, since systemd-boot picks the kernel image to boot
// by that order: each of these is newer than every one before it, as systemd's own test of
// strverscmp_improved lists them.
func TestVersionsAreOrderedAsSystemdOrdersThem(t *testing.T) {
	ordered := []string{
		"~1", "ab", "abb", "abc", "0001", "002", "12", "122", "122.9", "123~rc1", "123", "123-a",
		"123-a.1", "123-a1", "123-a1.1", "123-3", "123-3.1", "123^patch1", "123^1", "123.a-1", "123.1-1",
		"123a-1", "124",
	}
	for i, older := range ordered {
		for _, newer := range ordered[i+1:] {
			if got := Compare(newer, older); got != 1 {
				t.Errorf("Compare(%q, %q) = %d, want 1", newer, older, got)
			}
			if got := Compare(older, newer); got != -1 {
				t.Errorf("Compare(%q, %q) = %d, want -1", older, newer, got)
			}
		}
		if got := Compare(older, older); got != 0 {
			t.Errorf("Compare(%q, %q) = %d, want 0", older, older, got)
		}
	}
}

// And the pairs systemd's test holds, each newer than the other.
func TestTheNewerOfTwoVersionsIsSystemds(t *testing.T) {
	for _, tc := range []struct{ newer, older string }{
		{"123.45-67.89", "123.45-67.88"},
		{"123.45-67.89a", "123.45-67.89"},
		{"123.45-67.89", "123.45-67.ab"},
		{"123.45-67.89", "123.45-67.9"},
		{"123.45-67.89", "123.45-67"},
		{"123.45-67.89", "123.45-66.89"},
		{"123.45-67.89", "123.45-9.99"},
		{"123.45-67.89", "123.42-99.99"},
		{"123.45-67.89", "123-99.99"},
		{"123.45-67.89", "123~rc1-99.99"},
		{"123-45.67.89", "123~rc1-99.99"},
		{"123~rc2-67.89", "123~rc1-99.99"},
		{"123^aa2-67.89", "123~rc1-99.99"},
		{"123aa2-67.89", "123~rc1-99.99"},
		{"123^aa2-67.89", "123-99.99"},
		{"123aa2-67.89", "123-99.99"},
		{"123.45-67.89", "123^45-67.89"},
		{"123^aa2-67.89", "123^aa1-99.99"},
		{"123aa2-67.89", "123^aa2-67.89"},
		{"123.aa2-67.89", "123.aa1-99.99"},
		{"123aa2-67.89", "123.aa2-67.89"},
		// Letters of either case, and a segment of letters past the first, longer than another.
		{"123.B", "123.A"},
		{"123Z", "123"},
		{"1.abc", "1.ab"},
		{"1.b", "1.abc"},
		// Bare metal's own: a commit's time, a later commit's, and the update lane's builds after
		// one, which add a segment.
		{"20260930.085458", "20260930.051343"},
		{"20261001.000000", "20260930.235959"},
		{"20260930.051343.1", "20260930.051343"},
		{"20260930.051343.2", "20260930.051343.1"},
		{"20260930.051343.10", "20260930.051343.9"},
	} {
		if got := Compare(tc.newer, tc.older); got != 1 {
			t.Errorf("Compare(%q, %q) = %d, want 1", tc.newer, tc.older, got)
		}
		if got := Compare(tc.older, tc.newer); got != -1 {
			t.Errorf("Compare(%q, %q) = %d, want -1", tc.older, tc.newer, got)
		}
	}
}

// What is not a version character is skipped, and leading zeros are no part of a number.
func TestWhatIsNotAVersionIsSkipped(t *testing.T) {
	for _, tc := range [][2]string{
		{"123_aa2-67.89", "123aa+2-67.89"},
		{"1.0010", "1.10"},
		{"20260930.051343", "20260930.51343"},
	} {
		if got := Compare(tc[0], tc[1]); got != 0 {
			t.Errorf("Compare(%q, %q) = %d, want 0", tc[0], tc[1], got)
		}
	}
	if Compare("", "1") != -1 || Compare("1", "") != 1 || Compare("", "") != 0 {
		t.Error("no version is not older than any")
	}
}

// The desktop's versions as cmd/image usb writes them - a release's day and build number,
// 20261001.2, or a development build's UTC time, 20261001.134321 - are ordered by the day, then by
// the number: a day's 10th release after its 9th, and a development build after the day's releases.
func TestTheDesktopsVersionsAreOrderedByTime(t *testing.T) {
	ordered := []string{"20260930.235959", "20261001.1", "20261001.9", "20261001.10", "20261001.090000", "20261001.134321", "20261002.1", "20261002.010101", "20270101.000000"}
	for i, older := range ordered {
		for _, newer := range ordered[i+1:] {
			if Compare(newer, older) != 1 || Compare(older, newer) != -1 {
				t.Errorf("%s is not newer than %s", newer, older)
			}
		}
	}
}
