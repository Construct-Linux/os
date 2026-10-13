package main

import (
	"testing"

	"go.uber.org/goleak"
)

// The harness runs a film's screenshots, a dev VM's wait and the lifecycle's update CDN on
// goroutines: one still running after the tests is a server, a VM or a ticker nothing stopped.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
