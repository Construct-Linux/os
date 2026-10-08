package main

import "testing"

// ssh's own words for a connection that never reached the guest are retried; a command that
// failed, or a connection lost while one ran, is not.
func TestNotConnected(t *testing.T) {
	for stderr, want := range map[string]bool{
		"Connection timed out during banner exchange\r\nConnection to UNKNOWN port 0 timed out":                                    true,
		"kex_exchange_identification: read: Connection reset by peer\r\nConnection reset by UNKNOWN port 0 during banner exchange": true,
		"ssh: connect to host vsock/4207 port 22: Connection refused":                                                              true,
		"ssh: connect to host vsock/4207 port 22: Connection timed out":                                                            true,
		"Timeout, server UNKNOWN not responding.":                                                                                  false,
		"cat: /run/user/1000/construct-test/keyd.txt: No such file or directory":                                                   false,
		"": false,
	} {
		if got := notConnected(stderr); got != want {
			t.Errorf("notConnected(%q) = %v, want %v", stderr, got, want)
		}
	}
}
