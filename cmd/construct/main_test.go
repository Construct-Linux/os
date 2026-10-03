package main

import (
	"bufio"
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/Construct-Linux/os/internal/update"
)

// An install goes on only when the person types the disk's name back: anything else - another
// disk's name, yes, nothing - writes nothing.
func TestAnInstallAsksForTheDisksName(t *testing.T) {
	disk := update.BlockDisk{Name: "nvme0n1", Size: 1 << 40, Model: "Samsung SSD 990 PRO 1TB"}
	var out bytes.Buffer
	if err := confirm(bufio.NewReader(strings.NewReader("nvme0n1\n")), &out, disk); err != nil {
		t.Errorf("the disk's name was not taken: %v", err)
	}
	if !strings.Contains(out.String(), "Samsung SSD 990 PRO 1TB") || !strings.Contains(out.String(), "erased") {
		t.Errorf("the prompt is %q", out.String())
	}
	for _, typed := range []string{"sda\n", "yes\n", "\n", ""} {
		if err := confirm(bufio.NewReader(strings.NewReader(typed)), &out, disk); err == nil {
			t.Errorf("%q was taken for the disk's name", typed)
		}
	}
}

// The graphical installer sees whole percents, each once, per file; a file of unknown length
// shows none.
func TestPercents(t *testing.T) {
	var b strings.Builder
	p := percents(slog.New(slog.NewJSONHandler(&b, &slog.HandlerOptions{ReplaceAttr: dropTime})))
	for _, done := range []int64{0, 0, 1, 50, 50, 100} {
		p("root", done, 100)
	}
	p("verity", 5, -1)
	if got, want := strings.Count(b.String(), `"msg":"progress"`), 4; got != want {
		t.Errorf("%d progress records, want %d:\n%s", got, want, b.String())
	}
	if !strings.Contains(b.String(), `{"level":"INFO","msg":"progress","file":"root","percent":100}`) {
		t.Errorf("no 100%%:\n%s", b.String())
	}
}
