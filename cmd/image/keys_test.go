package main

import "testing"

// A test build is signed with files alone: a release's key (KMS) is refused for it, and taken for
// a release.
func TestRefuseKMS(t *testing.T) {
	if err := refuseKMS(built{Version: "20261004.1", Test: true}, "kms:alias/construct-release"); err == nil {
		t.Error("a test build with a KMS key: no error")
	}
	if err := refuseKMS(built{Version: "20261004.1", Test: true}, ".cache/test-keys/pcr.pem"); err != nil {
		t.Errorf("a test build with a file: %v", err)
	}
	if err := refuseKMS(built{Version: "20261004.1", Release: true}, "kms:alias/construct-pcr"); err != nil {
		t.Errorf("a release with a KMS key: %v", err)
	}
	if err := refuseKMS(built{Version: "20261004.150701"}, "kms:alias/construct-release"); err == nil {
		t.Error("a build without a release's version, with a KMS key: no error")
	}
}
