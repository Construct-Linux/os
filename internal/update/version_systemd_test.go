package update

import (
	"errors"
	"math/rand/v2"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// systemdCompare is how systemd itself orders a and b: systemd-analyze compare-versions prints
// "a < b", "a == b" or "a > b" and exits 12, 0 or 11. It answers false when the tool cannot
// answer, which the caller turns into a skip.
func systemdCompare(t testing.TB, a, b string) (int, bool) {
	t.Helper()
	// "--" because a version may start with '-', which would be read as an option.
	err := exec.Command("systemd-analyze", "compare-versions", "--", a, b).Run()
	if err == nil {
		return 0, true
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return 0, false
	}
	switch exit.ExitCode() {
	case 11:
		return 1, true
	case 12:
		return -1, true
	}
	return 0, false
}

// requireSystemd skips a test on a machine whose systemd-analyze is missing or too old to have
// compare-versions (systemd 252); GitHub's ubuntu-latest has it, so the check workflow runs it.
func requireSystemd(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("no systemd-analyze")
	}
	if r, ok := systemdCompare(t, "1", "2"); !ok || r != -1 {
		t.Skip("systemd-analyze has no compare-versions")
	}
}

// versionCorpus is the hard cases of systemd's ordering: pre-releases, the separators against
// each other and against characters that are none, leading zeros, letters against digits, empty
// segments, and the versions this desktop, its kernel and GNOME carry.
var versionCorpus = []string{
	"", "0", "00", "1", "01", "001", "1.0", "1.00", "1.0.0", "1.01", "1.1", "1.10", "1.9",
	"10", "2", "9", "a", "A", "Z", "z", "aa", "ab", "a1", "1a", "1A", "1z", "1.a", "1.A",
	"~", "~~", "1~", "1~~", "~1", "1~rc1", "1~rc2", "1~rc10", "1~beta", "1~alpha", "1~~rc1",
	"1^", "1^1", "1^a", "1^^", "1^~", "1~^", "1-", "1-1", "1-a", "1--1", "1.", "1..1", "1.-1",
	"1-.1", "1.^1", "1^.1", "1^-1", "1-^1", "-1", ".1", "^1", "1.~1", "1-~1", "1^~1",
	"1_1", "1+1", "1+", "1 1", "1:1", "1@1", "_1", "+", "_", "1._1", "1_.1", "1+a",
	"0001.0001", "0.0", "0.00.0", "00a", "a00", "a0", "0a",
	"123", "123a", "123-a", "123.a", "123^a", "123~a", "123aa2-67.89", "123^aa2-67.89",
	"20261013", "20261013.1", "20261013.2", "20261013.9", "20261013.10", "20261013.01",
	"20261013.1.1", "20261013.134321", "20261014.1", "20261013.1~rc1", "20261013.1^1",
	"20261013.1+3-1", "20261013.1+0-3", "20261013.1+2",
	"6.12", "6.12.0", "6.12.1", "6.12.10", "6.12.9", "7.2.10", "7.2.9", "7.2", "7.2-rc1",
	"7.2~rc1", "7.2.10-1", "7.2.10-arch1-1", "6.6.58-1-lts", "5.15.0-91-generic",
	"47.0", "47.1", "47.alpha", "47.beta", "47.rc", "47~alpha", "47~beta", "47~rc", "48.alpha.1",
	"3.38.6", "3.40", "40.0", "49.2", "50~alpha",
	"18446744073709551616", "18446744073709551615", "99999999999999999999999", "0018446744073709551616",
}

// agreesWithSystemd checks that Compare orders every two of versions as systemd-analyze does,
// asking systemd only about neighbours: one process per pair would take minutes. Sorted by
// Compare, the list is checked in Go to be a total order of Compare's - every earlier version
// older or equal, equal exactly when every step between is - and systemd is asked whether each
// step is the same step for it. Since systemd's order is an order too, agreeing on every step is
// agreeing on every pair.
func agreesWithSystemd(t *testing.T, versions []string) {
	t.Helper()
	sorted := slices.Clone(versions)
	slices.SortStableFunc(sorted, Compare)
	for i, a := range sorted {
		strict := false
		for j := i + 1; j < len(sorted); j++ {
			b := sorted[j]
			strict = strict || Compare(sorted[j-1], b) != 0
			want := 0
			if strict {
				want = -1
			}
			if got := Compare(a, b); got != want {
				t.Fatalf("Compare is no order: Compare(%q, %q) = %d, its neighbours say %d", a, b, got, want)
			}
			if got := Compare(b, a); got != -want {
				t.Fatalf("Compare is no order: Compare(%q, %q) = %d, its neighbours say %d", b, a, got, -want)
			}
		}
	}
	for i := 1; i < len(sorted); i++ {
		a, b := sorted[i-1], sorted[i]
		want, ok := systemdCompare(t, a, b)
		if !ok {
			t.Fatalf("systemd-analyze compare-versions %q %q failed", a, b)
		}
		if got := Compare(a, b); got != want {
			t.Errorf("Compare(%q, %q) = %d, systemd says %d", a, b, got, want)
		}
	}
}

// Compare orders every two versions of the corpus as systemd-analyze, so systemd's own
// strverscmp_improved, orders them: systemd-boot and systemd-sysupdate pick by that order, and an
// installer that disagrees picks another release than they do.
func TestCompareAgreesWithSystemdOnTheCorpus(t *testing.T) {
	requireSystemd(t)
	agreesWithSystemd(t, versionCorpus)
}

// randomVersion is a short string of the characters a release's file names admit
// (versionPattern), now and then one they do not, so runs of each class and the separators meet
// in combinations nobody listed.
func randomVersion(r *rand.Rand) string {
	const admitted = "00129abzAZ.-^~"
	const other = "_+ :"
	var s strings.Builder
	for range r.IntN(9) {
		if r.IntN(10) == 0 {
			s.WriteByte(other[r.IntN(len(other))])
		} else {
			s.WriteByte(admitted[r.IntN(len(admitted))])
		}
	}
	return s.String()
}

// Compare orders random versions as systemd does too. The seed is fixed so a failure repeats;
// FuzzCompare is where new inputs are looked for.
func TestCompareAgreesWithSystemdOnRandomVersions(t *testing.T) {
	requireSystemd(t)
	r := rand.New(rand.NewPCG(20261013, 1))
	prefixes := []string{"", "1", "1.2", "20261013.", "7.2-", "47~"}
	versions := make([]string, 1000)
	for i := range versions {
		// A common prefix now and then, so comparisons reach past the first segment.
		versions[i] = prefixes[r.IntN(len(prefixes))] + randomVersion(r)
	}
	agreesWithSystemd(t, versions)
}

// Compare is an order: a against b is b against a reversed, a version is equal to itself, and,
// where systemd-analyze is installed, it agrees with systemd.
func FuzzCompare(f *testing.F) {
	for _, s := range versionCorpus {
		f.Add(s, "1")
		f.Add("1"+s, s)
	}
	f.Add("123~rc1-99.99", "123^aa2-67.89")
	f.Add("20261013.1", "20261013.10")
	_, err := exec.LookPath("systemd-analyze")
	haveSystemd := err == nil
	if haveSystemd {
		if r, ok := systemdCompare(f, "1", "2"); !ok || r != -1 {
			haveSystemd = false
		}
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		ab, ba := Compare(a, b), Compare(b, a)
		if ab != -ba {
			t.Fatalf("Compare(%q, %q) = %d but Compare(%q, %q) = %d", a, b, ab, b, a, ba)
		}
		if Compare(a, a) != 0 {
			t.Fatalf("Compare(%q, %q) != 0", a, a)
		}
		// An argument cannot carry a NUL; the C string would end there.
		if !haveSystemd || strings.ContainsRune(a, 0) || strings.ContainsRune(b, 0) {
			return
		}
		want, ok := systemdCompare(t, a, b)
		if !ok {
			t.Fatalf("systemd-analyze compare-versions %q %q failed", a, b)
		}
		if ab != want {
			t.Fatalf("Compare(%q, %q) = %d, systemd says %d", a, b, ab, want)
		}
	})
}
