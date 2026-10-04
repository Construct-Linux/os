package main

import "testing"

// The peak is the sample with the most in use and in swap together; a line that is not four
// numbers is skipped.
func TestMemPeak(t *testing.T) {
	peak, n := memPeak("12.5 2000000 0 6000000\n30.0 3500000 100000 6000000\ngarbage\n45.1 3000000 900000 6000000\n")
	if n != 3 || peak.at != 45.1 || peak.used != 3000000 || peak.swap != 900000 {
		t.Errorf("peak %+v of %d", peak, n)
	}
}
