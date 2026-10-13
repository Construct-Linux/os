package update

import (
	"testing"

	"go.uber.org/goleak"
)

// writePartition feeds xz from the download through os/exec's copying goroutine, which only
// Wait ends: one left running after the tests is an xz, and a download, nothing waited for.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
