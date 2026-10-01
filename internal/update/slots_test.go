package update

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/spin-stack/spin-desktop/internal/gpt"
)

// A disk's slots are its root partitions, each with the verity partition labelled alike; a label
// that is neither empty nor a version, and a partition with no partner, are no layout an update
// writes into.
func TestSlotsArePairsOfPartitionsLabelledAlike(t *testing.T) {
	slots, err := Slots(testLayout())
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 2 || slots[0].Version != "1.0" || slots[0].Root.UUID != rootA || slots[0].Verity.UUID != verityA ||
		slots[1].Version != "" || slots[1].Label != Empty || slots[1].Root.UUID != emptyRoot || slots[1].Verity.UUID != emptyHash {
		t.Errorf("the slots are %+v", slots)
	}
	for name, change := range map[string]func(*gpt.Table){
		"a label of no version": func(tb *gpt.Table) { tb.Partitions[1].Name, tb.Partitions[2].Name = "root", "root" },
		"another image's label": func(tb *gpt.Table) { tb.Partitions[1].Name, tb.Partitions[2].Name = "fedora_40", "fedora_40" },
		"a version alone":       func(tb *gpt.Table) { tb.Partitions[1].Name, tb.Partitions[2].Name = "1.0", "1.0" },
		"the image of no version": func(tb *gpt.Table) {
			tb.Partitions[1].Name, tb.Partitions[2].Name = ImageID+"_x", ImageID+"_x"
		},
		"a root with no tree": func(tb *gpt.Table) { tb.Partitions[2].Name = ImageID + "_0.9" },
		"a tree with no root": func(tb *gpt.Table) { tb.Partitions[3].Type = gpt.TypeLinuxData },
	} {
		tb := testLayout()
		change(&tb)
		if got, err := Slots(tb); err == nil {
			t.Errorf("a disk with %s has the slots %+v", name, got)
		}
	}
	// Two slots both empty - a disk no update has written yet, or one whose write was cut off
	// before its version was named - are each paired with a hash tree of their own, wherever the
	// table has them.
	tb := testLayout()
	p := tb.Partitions
	p[1].Name, p[2].Name = Empty, Empty
	tb.Partitions = []gpt.Partition{p[4], p[1], p[3], p[2], p[0], p[5]}
	slots, err = Slots(tb)
	if err != nil || len(slots) != 2 || slots[0].Verity.UUID != emptyHash || slots[1].Verity.UUID != verityA {
		t.Errorf("two empty slots are %+v, %v", slots, err)
	}
}

// The booted slot is the one under the running root, and it holds the version the root says; the
// slot written is never it, an empty one first, else the oldest version.
func TestTheSlotWrittenIsNeverTheBootedOne(t *testing.T) {
	slots, err := Slots(testLayout())
	if err != nil {
		t.Fatal(err)
	}
	a, b := slots[0], slots[1]
	booted, err := Booted(slots, []int64{a.Root.First, a.Verity.First}, "1.0")
	if err != nil || booted.Root.UUID != rootA {
		t.Fatalf("booted %+v, %v", booted, err)
	}
	if _, err := Booted(slots, []int64{a.Root.First, a.Verity.First}, "1.1"); err == nil {
		t.Error("a root that is not the version its slot says was taken")
	}
	if _, err := Booted(slots, []int64{a.Root.First, b.Verity.First}, "1.0"); err == nil {
		t.Error("a root over two slots' partitions was taken")
	}
	if got, err := Target(slots, booted); err != nil || got.Root.UUID != emptyRoot {
		t.Errorf("the target is %+v, %v", got, err)
	}
	b.Label, b.Version = ImageID+"_1.1", "1.1"
	third := Slot{Label: ImageID + "_0.9", Version: "0.9", Root: gpt.Partition{First: 1 << 30}}
	if got, err := Target([]Slot{b, third, a}, b); err != nil || got.Version != "0.9" {
		t.Errorf("with two versions not booted the target is %+v, %v: not the oldest", got, err)
	}
	if got, err := Target([]Slot{b, a}, b); err != nil || got.Version != "1.0" {
		t.Errorf("the target is %+v, %v", got, err)
	}
	empty := Slot{Label: Empty, Root: gpt.Partition{First: 2 << 30}}
	if got, err := Target([]Slot{b, third, empty, a}, b); err != nil || got.Label != Empty {
		t.Errorf("with an empty slot after an older one, the target is %+v, %v", got, err)
	}
	if got, err := Target([]Slot{b}, b); err == nil {
		t.Errorf("a disk of one slot has the target %+v", got)
	}
}

// The root's device is found through sysfs: its dm-verity device's two partitions, on one disk of
// 512-byte sectors. A root over two disks, over something that is no partition, or on a disk of
// 4096-byte sectors is refused.
func TestTheRootIsFoundThroughSysfs(t *testing.T) {
	m := testMachine(t)
	dev := unix.Mkdev(253, 0)
	got, err := FindRoot(m.sys, dev)
	if err != nil {
		t.Fatal(err)
	}
	if got.Disk != "/dev/nvme0n1" || len(got.Starts) != 2 || got.Starts[0] != slotAOffset/512 || got.Starts[1] != 13<<20/512 {
		t.Errorf("the root is %+v", got)
	}
	for name, change := range map[string]func(){
		"4096-byte sectors": func() { write(t, filepath.Join(m.sys, "devices", "nvme0n1", "queue", "logical_block_size"), "4096\n") },
		"no partition": func() {
			must(t, os.Remove(filepath.Join(m.sys, "devices", "nvme0n1", "nvme0n1p3", "partition")))
		},
		"two disks": func() {
			write(t, filepath.Join(m.sys, "devices", "nvme1n1", "nvme1n1p1", "partition"), "1")
			write(t, filepath.Join(m.sys, "devices", "nvme1n1", "nvme1n1p1", "start"), "2048")
			link(t, filepath.Join("..", "..", "devices", "nvme1n1", "nvme1n1p1"), filepath.Join(m.sys, "class", "block", "nvme1n1p1"))
			must(t, os.Remove(filepath.Join(m.sys, "dev", "block", "253:0", "slaves", "nvme0n1p3")))
			write(t, filepath.Join(m.sys, "dev", "block", "253:0", "slaves", "nvme1n1p1"), "")
		},
		"one partition": func() { must(t, os.Remove(filepath.Join(m.sys, "dev", "block", "253:0", "slaves", "nvme0n1p3"))) },
	} {
		m.boot(t, "1.0", slotAOffset/512, 13<<20/512)
		change()
		if got, err := FindRoot(m.sys, dev); err == nil {
			t.Errorf("a root with %s was found: %+v", name, got)
		}
	}
	if _, err := FindRoot(m.sys, unix.Mkdev(8, 1)); err == nil {
		t.Error("a root that is no device-mapper device was found")
	}
}

// A kernel image's name is its version and systemd-boot's count: blessed with none, being counted
// with tries left and done, bad once none are left. Anything else on the ESP is not bare metal's.
func TestAKernelImagesNameSaysItsTries(t *testing.T) {
	for name, want := range map[string]Image{
		ImageID + "_1.0.efi":     {Version: "1.0"},
		ImageID + "_1.1+3.efi":   {Version: "1.1", Counted: true, Left: 3},
		ImageID + "_1.1+1-2.efi": {Version: "1.1", Counted: true, Left: 1, Done: 2},
		ImageID + "_1.1+0-3.efi": {Version: "1.1", Counted: true, Done: 3},
	} {
		want.Name = name
		got, ok := ParseImage(name)
		if !ok || got != want {
			t.Errorf("ParseImage(%q) = %+v, %v; want %+v", name, got, ok, want)
		}
		if got.Bad() != (want.Counted && want.Left == 0) {
			t.Errorf("%s is bad: %v", name, got.Bad())
		}
	}
	for _, name := range []string{"spin-os-metal-installer_1.0.efi", "other_1.0.efi", ImageID + "_1.0.EFI", ImageID + "_1.0+x.efi", ImageID + "_.efi"} {
		if got, ok := ParseImage(name); ok {
			t.Errorf("ParseImage(%q) = %+v", name, got)
		}
	}
	// On the ESP, what is neither a file nor bare metal's kernel image is no image.
	esp := t.TempDir()
	write(t, filepath.Join(esp, Linux, ImageID+"_1.0+2-1.efi"), "")
	write(t, filepath.Join(esp, Linux, "other.efi"), "")
	must(t, os.MkdirAll(filepath.Join(esp, Linux, ImageID+"_1.1.efi"), 0o755))
	images, err := Images(esp)
	if err != nil || len(images) != 1 || images[0].Name != ImageID+"_1.0+2-1.efi" {
		t.Errorf("the ESP's images are %+v, %v", images, err)
	}
}

// What a reboot moves the machine to is the newest version on a slot, newer than the booted one,
// whose kernel image is there and not bad.
func TestTheVersionPendingIsTheNewestBootable(t *testing.T) {
	slots := []Slot{{Version: "1.0"}, {Version: "1.2"}, {Version: "1.1"}, {Version: ""}, {Version: "1.3"}}
	img := func(v string, left int) Image { return Image{Version: v, Counted: left >= 0, Left: max(left, 0)} }
	for _, tc := range []struct {
		name   string
		images []Image
		booted string
		want   string
	}{
		{"the newest, counted", []Image{img("1.0", -1), img("1.1", 3), img("1.2", 2)}, "1.0", "1.2"},
		{"the newest, after an older", []Image{img("1.1", 3), img("1.3", 2)}, "1.0", "1.3"},
		{"the newest bad", []Image{img("1.0", -1), img("1.1", 3), img("1.2", 0)}, "1.0", "1.1"},
		{"blessed", []Image{img("1.2", -1)}, "1.0", "1.2"},
		{"no image", []Image{img("1.0", -1)}, "1.0", ""},
		{"none newer", []Image{img("1.0", 3), img("1.1", 3), img("1.2", 3)}, "1.2", ""},
	} {
		if got := Pending(slots, tc.images, tc.booted); got != tc.want {
			t.Errorf("%s: Pending = %q, want %q", tc.name, got, tc.want)
		}
	}
}

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
		"another slot": {image: peImage(t, map[string]string{".osrel": "IMAGE_VERSION=1.1\n", ".cmdline": strings.ReplaceAll(good, verity, verityA)}),
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
