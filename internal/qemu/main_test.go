package qemu

import (
	"testing"

	"go.uber.org/goleak"
)

// A VM and swtpm are each waited for on a goroutine: one still running after the tests is a QEMU
// or swtpm that nothing stopped, which on the harness keeps a disk and a port held.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
