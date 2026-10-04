package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A machine follows stable until it is told otherwise; another channel is one the root has
// transfers for, written as construct-update.service's --component, and stable again removes it.
func TestChannel(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"usr/lib/sysupdate.d", "usr/lib/sysupdate.testing.d"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var out strings.Builder
	if err := channelCmd(nil, &out, root); err != nil || out.String() != "stable (of stable, testing)\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
	if err := channelCmd([]string{"nightly"}, &out, root); err == nil {
		t.Error("a channel without transfers: no error")
	}
	if err := channelCmd([]string{"testing"}, &out, root); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, channelFile))
	if !strings.Contains(string(b), "SYSUPDATE_COMPONENT=--component=testing\n") || currentChannel(root) != "testing" {
		t.Errorf("following testing: %q", b)
	}
	if err := channelCmd([]string{"stable"}, &out, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, channelFile)); !os.IsNotExist(err) || currentChannel(root) != "stable" {
		t.Errorf("back to stable: %v", err)
	}
}
