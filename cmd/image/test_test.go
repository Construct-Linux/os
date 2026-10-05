package main

import "testing"

// Both units active and rated 2.0 or under; a third line, an inactive unit, a higher score or a
// missing one fails.
func TestSandboxedOK(t *testing.T) {
	for out, want := range map[string]bool{
		"cups-browsed active 1.6\ngeoclue active 1.3\n": true,
		"cups-browsed active 1.6\ngeoclue failed 1.3\n": false,
		"cups-browsed active 9.4\ngeoclue active 1.3\n": false,
		"cups-browsed active\ngeoclue active 1.3\n":     false,
		"cups-browsed active 1.6\n":                     false,
	} {
		if got := sandboxedOK(out); got != want {
			t.Errorf("%q: %v, want %v", out, got, want)
		}
	}
}
