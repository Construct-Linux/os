package update

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A boot that reached boot-complete.target is left alone; one that did not, of a kernel image
// systemd-boot counts, is rebooted, for systemd-boot to try it again or fall back; one that did not
// of a blessed image has nothing to fall back to, which is said.
func TestABootThatIsNotGoodIsRebootedWhileCounted(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	vars := Vars(t.TempDir())
	s := &fakeSystem{active: map[string]bool{BootComplete: true}}
	if err := BootCheck(t.Context(), s, vars, log); err != nil || s.reboots != 0 {
		t.Errorf("a good boot: %v, %d reboots", err, s.reboots)
	}
	s.active[BootComplete] = false
	if err := BootCheck(t.Context(), s, vars, log); !errors.Is(err, ErrNothingToFallBackTo) || s.reboots != 0 {
		t.Errorf("a blessed image's bad boot: %v, %d reboots", err, s.reboots)
	}
	path := `\EFI\Linux\` + ImageID + `_1.1+2-1.efi`
	must(t, vars.Write("LoaderBootCountPath", Loader, BootService|Runtime, UTF16(path)))
	if err := BootCheck(t.Context(), s, vars, log); err != nil || s.reboots != 1 {
		t.Errorf("a counted image's bad boot: %v, %d reboots", err, s.reboots)
	}
}

// A kernel image is held to naming the slot it is written for, and to being the version it is
// released as; what is not a PE image, or has no command line, is not one.
func TestAKernelImageNamesItsSlot(t *testing.T) {
	dir := t.TempDir()
	const root, verity = "0123abcd-0000-1111-2222-333344445555", "89abcdef-6666-7777-8888-9999aaaabbbb"
	good := "dm-mod.create=\"root,,,ro,0 8 verity 1 PARTUUID=" + root + " PARTUUID=" + verity + " 4096\" dm-mod.waitfor=PARTUUID=" + root + ",PARTUUID=" + verity + " root=/dev/dm-0"
	for name, tc := range map[string]struct {
		image []byte
		fails string
	}{
		"its own": {image: peImage(t, map[string]string{".osrel": "IMAGE_VERSION=1.1\n", ".cmdline": good})},
		"another version": {image: peImage(t, map[string]string{".osrel": "IMAGE_VERSION=1.10\n", ".cmdline": good}),
			fails: "os-release"},
		"another slot": {image: peImage(t, map[string]string{".osrel": "IMAGE_VERSION=1.1\n", ".cmdline": strings.ReplaceAll(good, verity, "3f3c0a48-7b27-5b3b-73c2-385422d45830")}),
			fails: verity},
		"no command line": {image: peImage(t, map[string]string{".osrel": "IMAGE_VERSION=1.1\n"}), fails: ".cmdline"},
		"no os-release":   {image: peImage(t, map[string]string{".cmdline": good}), fails: ".osrel"},
		"not PE":          {image: []byte("MZ nothing"), fails: "not a PE image"},
	} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		must(t, os.WriteFile(p, tc.image, 0o644))
		err := checkImage(p, "1.1", root, verity)
		if (err == nil) != (tc.fails == "") || (err != nil && !strings.Contains(err.Error(), tc.fails)) {
			t.Errorf("%s: %v, want an error saying %q", name, err, tc.fails)
		}
	}
}
