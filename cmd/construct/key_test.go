package main

import (
	"io"
	"strings"
	"testing"
)

// Off until turned on; on and off again; removed does nothing while it is off.
func TestLockOnRemoval(t *testing.T) {
	etc := t.TempDir()
	say := func() string {
		var b strings.Builder
		if err := keyLockOnRemoval(nil, &b, etc); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(b.String())
	}
	if say() != "off" {
		t.Error("on before it was asked for")
	}
	if err := keyRemoved(t.Context(), etc); err != nil {
		t.Errorf("removed while off: %v", err)
	}
	if err := keyLockOnRemoval([]string{"on"}, io.Discard, etc); err != nil || say() != "on" {
		t.Errorf("on: %v %s", err, say())
	}
	if err := keyLockOnRemoval([]string{"off"}, io.Discard, etc); err != nil || say() != "off" {
		t.Errorf("off: %v %s", err, say())
	}
	if err := keyLockOnRemoval([]string{"maybe"}, io.Discard, etc); err == nil {
		t.Error("maybe passed")
	}
}
