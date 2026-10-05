package main

import (
	"testing"
)

// The X9-15's panel at 200% is 1440x900 logical, and QEMU is asked for exactly that panel; 1x is
// 1280x800, and nothing else is a screen.
func TestScreenFor(t *testing.T) {
	s, err := screenFor(2)
	if err != nil || s != screenX9 {
		t.Fatalf("2: %+v %v", s, err)
	}
	if w, h := s.logical(); w != 1440 || h != 900 {
		t.Errorf("logical %dx%d", w, h)
	}
	if s, err := screenFor(1); err != nil || s != screen1x {
		t.Errorf("1: %+v %v", s, err)
	}
	if _, err := screenFor(3); err == nil {
		t.Error("-scale 3 accepted")
	}
}

// A screenshot over the stage is a whole scale or none: 150% is not measured.
func TestScaleOf(t *testing.T) {
	for _, c := range []struct {
		phys, stage, want int
		ok                bool
	}{{2880, 1440, 2, true}, {1280, 1280, 1, true}, {2880, 1920, 0, false}, {2880, 0, 0, false}} {
		if k, ok := scaleOf(c.phys, c.stage); k != c.want || ok != c.ok {
			t.Errorf("%d over %d: %d %v", c.phys, c.stage, k, ok)
		}
	}
}

// The tablet's scale spans the stage, whatever its size: its corners are 0 and 32767.
func TestAbsPoint(t *testing.T) {
	if x, y := absPoint(0, 0, 1440, 900); x != 0 || y != 0 {
		t.Errorf("origin %d,%d", x, y)
	}
	if x, y := absPoint(1439, 899, 1440, 900); x != 32767 || y != 32767 {
		t.Errorf("corner %d,%d", x, y)
	}
	if x, _ := absPoint(720, 0, 1440, 900); x < 16380 || x > 16400 {
		t.Errorf("middle %d", x)
	}
}

func TestGdctlScale(t *testing.T) {
	show := "Monitors:\n└──Monitor Virtual-1 (Virtual monitor)\nLogical monitors:\n└──Logical monitor #1\n   ├──Position: (0, 0)\n   ├──Scale: 2.0\n   ├──Transform: normal\n"
	if s, ok := gdctlScale(show); !ok || s != 2 {
		t.Errorf("%v %v", s, ok)
	}
	if _, ok := gdctlScale("Monitors:\n"); ok {
		t.Error("a scale read from no logical monitor")
	}
}

// Plymouth's guess, as its ply-utils.c makes it: 2 from 2880x1620, from 1800x1200 on 3:2, else 1.
func TestPlymouthScale(t *testing.T) {
	for _, c := range []struct{ w, h, want int }{
		{1280, 800, 1}, {2880, 1800, 2}, {1800, 2880, 2}, {2560, 1600, 1}, {1800, 1200, 2}, {1500, 1000, 1}, {3840, 2160, 2},
	} {
		if got := plymouthScale(c.w, c.h); got != c.want {
			t.Errorf("%dx%d: %d, want %d", c.w, c.h, got, c.want)
		}
	}
}

// The bar's fallback is where it was at 1280x800, and the same stage rectangle at 2x.
func TestNeedleBarFallback(t *testing.T) {
	if r := needleBarFallback(1280, 1); r.X != 640 || r.Y != 8 || r.W != 634 || r.H != 32 {
		t.Errorf("1x %+v", r)
	}
	if r := needleBarFallback(2880, 2); r.X != 1440 || r.Y != 16 || r.W != 1428 || r.H != 64 {
		t.Errorf("2x %+v", r)
	}
	if r := needleBarFallback(1280, 0); r.X != 640 {
		t.Errorf("no scale is 1: %+v", r)
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("a\nb\nc\n", 2); got != "b\nc" {
		t.Errorf("%q", got)
	}
	if got := lastLines("a", 5); got != "a" {
		t.Errorf("%q", got)
	}
}
