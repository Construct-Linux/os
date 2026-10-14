package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// task release asks for the harness's last run to stand for HEAD, on the stick: another commit, a
// checkout with changes, the ext4 disk, some scenarios or a failure each say what to run first.
func TestAReleaseNeedsTheStickHarnessPassedOnHead(t *testing.T) {
	const head = "0123456789abcdef0123456789abcdef01234567"
	good := tested{Commit: head, Clean: true, USB: true, Full: true, Passed: true}
	if err := good.covers(head, true); err != nil {
		t.Errorf("a full passed run on the stick: %v", err)
	}
	if err := good.covers(head, false); err != nil {
		t.Errorf("the stick's run stands for the ext4 disk's too: %v", err)
	}
	for name, c := range map[string]struct {
		rec  tested
		want string
	}{
		"another commit": {tested{Commit: strings.Repeat("f", 40), Clean: true, USB: true, Full: true, Passed: true}, "not on HEAD"},
		"changes":        {tested{Commit: head, USB: true, Full: true, Passed: true}, "not committed"},
		"the ext4 disk":  {tested{Commit: head, Clean: true, Full: true, Passed: true}, "test:usb first"},
		"some scenarios": {tested{Commit: head, Clean: true, USB: true, Passed: true}, "-only"},
		"a failure":      {tested{Commit: head, Clean: true, USB: true, Full: true}, "failed"},
	} {
		err := c.rec.covers(head, true)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// image test records what it ran however it ended: a failed run is on record as failed.
func TestTheHarnessRecordsItsRun(t *testing.T) {
	dir := t.TempDir()
	recordTested(dir, true, false, errors.New("3 checks failed"))
	b, err := os.ReadFile(filepath.Join(dir, testedFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"usb": true`, `"full": false`, `"passed": false`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("tested.json lacks %s:\n%s", want, b)
		}
	}
}
