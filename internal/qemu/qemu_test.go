package qemu

import "testing"

// The panel QEMU is asked for is the one the guest's virtio GPU reports; without one, QEMU's own
// default.
func TestGPU(t *testing.T) {
	if d := (Machine{Width: 2880, Height: 1800}).gpu(); d != "virtio-vga,xres=2880,yres=1800" {
		t.Errorf("2880x1800: %q", d)
	}
	if d := (Machine{}).gpu(); d != "virtio-vga" {
		t.Errorf("no panel: %q", d)
	}
}
