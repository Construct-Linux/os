package apkver

import "testing"

// The orderings apk-tools gives (apk version -t a b), the cases of knqyf263/go-apk-version#4
// among them: a pre-release suffix after a 0 component is older than its release.
func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.0_rc1", "1.0", -1},
		{"18.0_rc20260919", "18.0", -1},
		{"2.0_beta1", "2.0", -1},
		{"1.1_rc1", "1.1", -1},
		{"1.0", "1.0_rc1", 1},
		{"2.5.2-r1", "2.5.3-r0", -1},
		{"2.5.3-r0", "2.5.3-r1", -1},
		{"2.5.3-r1", "2.5.3-r1", 0},
		{"1.3.2.1_rc20260917-r0", "1.3.2.1_rc20260921-r0", -1},
		{"10.6_p1-r1", "10.6-r0", 1},
		{"1.10-r0", "1.9-r0", 1},
		{"1.0a", "1.0", 1},
		{"1.0a", "1.0b", -1},
		{"20230201-r30", "20230201-r4", 1},
		{"3.0_git20260101", "3.0", 1},
		{"1.2.3", "1.2.3.0", -1},
		{"1.2.3", "1.2", 1},
		{"1.0_rc1", "1.0_rc2", -1},
		{"1.0_alpha1", "1.0_beta1", -1},
		{"1.0_pre1", "1.0_rc1", -1},
		{"1.0_p1", "1.0_rc1", 1},
		{"1.0.07", "1.0.7", -1},
		{"1.0.07", "1.0.1", -1},
		{"1.0~abc", "1.0", 1},
		{"1.0-r1", "1.0", 1},
		{"51.0-r3", "51.0-r10", -1},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
