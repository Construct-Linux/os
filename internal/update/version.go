// Package update is the half of a machine's versions systemd does not do. The installer reads a
// signed release - SHA256SUMS and its OpenPGP signature by the key the root trusts - and lays the
// disk systemd-sysupdate's transfers expect: the slots labelled <IMAGE_ID>_<version> with the UUIDs
// the root hash gives them, the kernel image counted for systemd-boot, /var with the machine's
// UUID. Updates after that are systemd-sysupdate's; boot-check marks a release that came up good,
// and Pending tells the person a newer one waits for a reboot.
package update

// Compare orders two versions as systemd does (strverscmp_improved, the UAPI group's version
// format specification): systemd-boot orders kernel images by it, and systemd-sysupdate versions.
// It answers -1 when a is older than b, 1 when newer, 0 when they are the same version.
//
// A version is compared segment by segment: runs of digits as numbers, leading zeros aside, and of
// letters as strings, a run of digits being newer than one of letters; '~' before a segment makes
// it older than anything, even the end ("1~rc1" is older than "1"); '-', '^' and '.' separate
// segments, in that order of age; and past the last segment a version with more is newer ("1.1" is
// newer than "1", and any version than none). Any other character is skipped.
func Compare(a, b string) int {
	i, j := 0, 0
	for {
		for i < len(a) && !versionChar(a[i]) {
			i++
		}
		for j < len(b) && !versionChar(b[j]) {
			j++
		}
		ca, cb := at(a, i), at(b, j)
		if ca == '~' || cb == '~' {
			if r := order(boolInt(ca != '~'), boolInt(cb != '~')); r != 0 {
				return r
			}
			i, j = i+1, j+1
		}
		if i >= len(a) || j >= len(b) {
			return order(len(a)-i, len(b)-j)
		}
		for _, sep := range []byte{'-', '^', '.'} {
			ca, cb = at(a, i), at(b, j)
			if ca == sep || cb == sep {
				if r := order(boolInt(ca != sep), boolInt(cb != sep)); r != 0 {
					return r
				}
				i, j = i+1, j+1
			}
		}
		var r int
		if r, i, j = segment(a, b, i, j); r != 0 {
			return r
		}
	}
}

// segment compares the segments of a and b at i and j - digits when either is, letters otherwise
// - and answers the order and where each string's next segment starts.
func segment(a, b string, i, j int) (int, int, int) {
	if digit(at(a, i)) || digit(at(b, j)) {
		ea, eb := run(a, i, digit), run(b, j, digit)
		// A number is newer than no number: "1.1" against "1.a".
		if r := order(boolInt(ea > i), boolInt(eb > j)); r != 0 {
			return r, ea, eb
		}
		for i < ea && a[i] == '0' {
			i++
		}
		for j < eb && b[j] == '0' {
			j++
		}
		// Without leading zeros the longer number is the larger, and two of one length compare
		// as strings.
		if r := order(ea-i, eb-j); r != 0 {
			return r, ea, eb
		}
		return compareStrings(a[i:ea], b[j:eb]), ea, eb
	}
	ea, eb := run(a, i, letter), run(b, j, letter)
	n := min(ea-i, eb-j)
	if r := compareStrings(a[i:i+n], b[j:j+n]); r != 0 {
		return r, ea, eb
	}
	return order(ea-i, eb-j), ea, eb
}

// run is where the run of characters class takes, from i in s, ends.
func run(s string, i int, class func(byte) bool) int {
	for i < len(s) && class(s[i]) {
		i++
	}
	return i
}

// at is s[i], or 0 past its end, as a C string reads.
func at(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0 // mutate-exempt: any byte that is no version character reads as the end
}

func versionChar(c byte) bool {
	return digit(c) || letter(c) || c == '~' || c == '-' || c == '^' || c == '.'
}

func digit(c byte) bool  { return c >= '0' && c <= '9' }
func letter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func order(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func boolInt(b bool) int {
	if b {
		return 1 // mutate-exempt: any true above false orders the same
	}
	return 0
}
