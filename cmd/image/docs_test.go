package main

import (
	"reflect"
	"testing"
)

func TestMissingDocs(t *testing.T) {
	db := []apkPackage{
		{Name: "procps", Files: []string{"usr/bin/top"}},
		{Name: "coreutils", Files: []string{"usr/bin/ls"}},
		{Name: "coreutils-doc", Files: []string{"usr/share/man/man1/ls.1"}},
		// A library: its -doc is an API reference, not a manual a person runs man for.
		{Name: "glibmm", Files: []string{"usr/lib/libglibmm-2.68.so.1"}},
		{Name: "gtk-4", Files: []string{"usr/bin/gtk4-builder-tool"}},
		{Name: "sudo", Files: []string{"usr/sbin/visudo"}},
		{Name: "nodoc", Files: []string{"usr/bin/nodoc"}},
	}
	available := map[string]bool{
		"procps-doc": true, "coreutils-doc": true, "glibmm-doc": true, "gtk-4-doc": true, "sudo-doc": true,
	}
	got := missingDocs(db, available)
	if want := []string{"procps-doc", "sudo-doc"}; !reflect.DeepEqual(got, want) {
		t.Errorf("missingDocs = %v, want %v", got, want)
	}
}
