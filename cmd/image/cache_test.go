package main

import (
	"strings"
	"testing"
)

// A cached build's key takes the pins its build reads from build/versions.env, each as its
// line, and refuses a pin the file does not set; every cacheable's pins are set there.
func TestPinLines(t *testing.T) {
	env := "# pins\nKERNEL_VERSION=7.2.10\nGRYPE_IMAGE=anchore/grype:latest@sha256:0\nKERNEL_SHA256=abc\n"
	got, err := pinLines(env, []string{"KERNEL_SHA256", "KERNEL_VERSION"})
	if err != nil || strings.Join(got, " ") != "KERNEL_SHA256=abc KERNEL_VERSION=7.2.10" {
		t.Errorf("pinLines: %v, %v", got, err)
	}
	if _, err := pinLines(env, []string{"QEMU_VERSION"}); err == nil {
		t.Error("a pin the file does not set passed")
	}
	t.Chdir("../..")
	for _, c := range cacheable {
		if _, err := cacheKey(c); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
