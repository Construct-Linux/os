package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Construct-Linux/os/internal/power"
)

// A night asleep reads as hours, the share of it in the low-power state, and the battery's cost.
func TestSleepSummary(t *testing.T) {
	s := power.Sleep{Slept: 8 * time.Hour, HWSleep: 6 * time.Hour, UsedWh: 2.4, FullWh: 60}
	got := sleepSummary(s)
	want := "Slept 8h0m0s, the hardware in its low-power state 75% of it; used 2.40 Wh, 0.50% of the battery an hour."
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	// A machine without a battery says nothing about one.
	if got := sleepSummary(power.Sleep{Slept: time.Minute}); strings.Contains(got, "battery") {
		t.Errorf("no battery, yet: %q", got)
	}
}

func TestDescribePowerWithoutBattery(t *testing.T) {
	now := time.Now()
	got := describePower(power.Snapshot{Time: now}, power.Snapshot{Time: now.Add(time.Second)})
	if !strings.HasPrefix(got, "No battery") || !strings.Contains(got, "sudo construct power") {
		t.Errorf("%q", got)
	}
}

// A short sleep is told in seconds, not rounded to nothing.
func TestShortSleep(t *testing.T) {
	if got := sleepSummary(power.Sleep{Slept: 21 * time.Second}); !strings.HasPrefix(got, "Slept 21s,") {
		t.Errorf("%q", got)
	}
}
