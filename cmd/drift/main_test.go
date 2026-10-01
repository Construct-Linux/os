package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spin-stack/spindrift/internal/update"
)

// An install goes on only when the person types the disk's name back: anything else - another
// disk's name, yes, nothing - writes nothing.
func TestAnInstallAsksForTheDisksName(t *testing.T) {
	disk := update.BlockDisk{Name: "nvme0n1", Size: 1 << 40, Model: "Samsung SSD 990 PRO 1TB"}
	var out bytes.Buffer
	if err := confirm(strings.NewReader("nvme0n1\n"), &out, disk); err != nil {
		t.Errorf("the disk's name was not taken: %v", err)
	}
	if !strings.Contains(out.String(), "Samsung SSD 990 PRO 1TB") || !strings.Contains(out.String(), "erased") {
		t.Errorf("the prompt is %q", out.String())
	}
	for _, typed := range []string{"sda\n", "yes\n", "\n", ""} {
		if err := confirm(strings.NewReader(typed), &out, disk); err == nil {
			t.Errorf("%q was taken for the disk's name", typed)
		}
	}
}
