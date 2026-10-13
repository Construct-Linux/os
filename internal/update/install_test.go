package update

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Construct-Linux/os/internal/gpt"
)

// installSize is the disk a test installs on: the least the layout takes, and a gibibyte more.
const installSize = (firstSector+espSectors+2*(rootSectors+veritySectors))*gpt.Sector + minVar + 1<<30

// installer is an install's machine: a sparse disk, the ESP's directory, the firmware's variables
// and the programs it ran.
type installer struct {
	disk *os.File
	esp  string
	vars Vars
	ran  []string
}

func newInstaller(t *testing.T, size int64) *installer {
	t.Helper()
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "disk"))
	must(t, err)
	t.Cleanup(func() { _ = f.Close() })
	must(t, f.Truncate(size))
	vars := Vars(filepath.Join(dir, "efivars"))
	must(t, os.MkdirAll(string(vars), 0o755))
	return &installer{disk: f, esp: filepath.Join(dir, "esp"), vars: vars}
}

func (in *installer) options(t *testing.T, source string, size int64) InstallOptions {
	t.Helper()
	stick := t.TempDir()
	write(t, filepath.Join(stick, "EFI/systemd/systemd-bootx64.efi"), "systemd-boot")
	write(t, filepath.Join(stick, "loader/keys/auto/db.auth"), "db")
	n := 0
	return InstallOptions{
		Keys: trusted(t), Source: source,
		Disk:      BlockDisk{Name: "nvme0n1", Size: size},
		Device:    in.disk,
		Reread:    func() error { return nil },
		Partition: func(n int) (string, error) { return fmt.Sprintf("/dev/nvme0n1p%d", n), nil },
		Run: func(_ context.Context, name string, args ...string) error {
			in.ran = append(in.ran, name+" "+strings.Join(args, " "))
			return nil
		},
		MountESP: func(context.Context, string) (string, func() error, error) {
			must(t, os.MkdirAll(in.esp, 0o755))
			return in.esp, func() error { return nil }, nil
		},
		MakeVar: func(_ context.Context, partition string) error {
			in.ran = append(in.ran, "make /var on "+partition)
			return nil
		},
		BootFrom: stick, Vars: in.vars,
		VarUUID: testVarUUID,
		UUID:    func() string { n++; return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) },
		Log:     slog.New(slog.DiscardHandler),
	}
}

// An install lays the disk out as systemd-sysupdate's transfers read it - an ESP, slot A holding the release under the
// UUIDs and label its files name, slot B empty, /var - writes the release's root and hash tree into
// slot A, makes the ESP and /var, gives the ESP systemd-boot and the kernel image blessed, and makes
// the firmware boot the disk first.
func TestAnInstallLaysOutTheDiskAsAnUpdateReadsIt(t *testing.T) {
	in := newInstaller(t, installSize)
	dir, body := release(t, "20261001.134321", newRoot, newVerity, signer(), 0xb)
	o := in.options(t, dir, installSize)
	if err := Install(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	// The layout of the same release with the same GUIDs, numbered: what the disk must hold now.
	r, _, err := newest(t.Context(), dir, o.Keys)
	must(t, err)
	laid, err := layout(r, installSize, testVarUUID, in.options(t, dir, installSize).UUID)
	must(t, err)
	read, err := gpt.Read(in.disk)
	must(t, err)
	if !reflect.DeepEqual(read, laid.table) {
		t.Errorf("the disk's table is %+v, not the layout's %+v", read, laid.table)
	}
	label := ImageID + "_20261001.134321"
	var got []string
	for _, p := range read.Partitions {
		got = append(got, p.Type+" "+p.Name)
	}
	want := []string{gpt.TypeESP + " " + espName, typeRoot + " " + label, typeRootVerity + " " + label,
		typeRoot + " " + Empty, typeRootVerity + " " + Empty, typeVar + " " + varName}
	if !slices.Equal(got, want) {
		t.Errorf("the partitions are %v", got)
	}
	if read.Partitions[5].UUID != testVarUUID {
		t.Errorf("/var's UUID is %s, not its machine's", read.Partitions[5].UUID)
	}
	if read.Partitions[1].UUID != newRoot || read.Partitions[2].UUID != newVerity {
		t.Errorf("slot A's UUIDs are %s and %s", read.Partitions[1].UUID, read.Partitions[2].UUID)
	}
	slotA := make([]byte, len(body))
	_, err = in.disk.ReadAt(slotA, read.Partitions[1].Offset())
	must(t, err)
	if !bytes.Equal(slotA, body) {
		t.Error("slot A does not hold the release's root")
	}
	if !slices.Equal(in.ran, []string{"mkfs.vfat -F 32 -n ESP /dev/nvme0n1p1", "make /var on /dev/nvme0n1p6"}) {
		t.Errorf("ran %v", in.ran)
	}
	for _, f := range []string{"EFI/BOOT/BOOTX64.EFI", "EFI/systemd/systemd-bootx64.efi", "loader/loader.conf", "loader/keys/auto/db.auth", linuxDir + "/" + label + ".efi"} {
		if _, err := os.Stat(filepath.Join(in.esp, f)); err != nil {
			t.Errorf("the ESP lacks %s", f)
		}
	}
	if images, _ := os.ReadDir(filepath.Join(in.esp, linuxDir)); len(images) != 1 || images[0].Name() != label+".efi" {
		t.Errorf("the ESP's kernel images are %v, not %s.efi alone, blessed", images, label)
	}
	order, err := in.vars.Read("BootOrder", Global)
	if err != nil || len(order) != 2 || binary.LittleEndian.Uint16(order) != 0 {
		t.Errorf("BootOrder is %v, %v", order, err)
	}
}

// A disk too small for two slots and a /var worth having is refused before anything is written.
func TestASmallDiskIsRefused(t *testing.T) {
	const small = 20 << 30
	in := newInstaller(t, small)
	dir, _ := release(t, "20261001.134321", newRoot, newVerity, signer(), 0xb)
	if err := Install(t.Context(), in.options(t, dir, small)); err == nil || !strings.Contains(err.Error(), "needs") {
		t.Fatalf("err = %v", err)
	}
	head := make([]byte, 1<<20)
	_, err := in.disk.ReadAt(head, 0)
	must(t, err)
	if !bytes.Equal(head, make([]byte, len(head))) || len(in.ran) != 0 {
		t.Error("a disk too small was written")
	}
}

// A release signed by another key installs nothing: the disk is as it was.
func TestAnInstallOfAnotherKeysReleaseWritesNothing(t *testing.T) {
	in := newInstaller(t, installSize)
	dir, _ := release(t, "20261001.134321", newRoot, newVerity, otherKey(), 0xb)
	if err := Install(t.Context(), in.options(t, dir, installSize)); err == nil {
		t.Fatal("another key's release was installed")
	}
	head := make([]byte, 1<<20)
	_, err := in.disk.ReadAt(head, 0)
	must(t, err)
	if !bytes.Equal(head, make([]byte, len(head))) || len(in.ran) != 0 {
		t.Error("the disk was written")
	}
}

// A root whose published bytes are not what SHA256SUMS says - changed after it was signed - is a
// slot A left Empty: nothing boots it, and no kernel image is laid.
func TestAnInstallOfAChangedRootLeavesSlotAEmpty(t *testing.T) {
	in := newInstaller(t, installSize)
	dir, _ := release(t, "20261001.134321", newRoot, newVerity, signer(), 0xb)
	roots, _ := filepath.Glob(filepath.Join(dir, "*.root.raw.xz"))
	f, err := os.OpenFile(roots[0], os.O_WRONLY, 0)
	must(t, err)
	_, err = f.WriteAt([]byte{0xff}, 100)
	must(t, err)
	must(t, f.Close())
	if err := Install(t.Context(), in.options(t, dir, installSize)); err == nil {
		t.Fatal("a changed root was installed")
	}
	read, err := gpt.Read(in.disk)
	must(t, err)
	if read.Partitions[1].Name != Empty || read.Partitions[2].Name != Empty {
		t.Errorf("slot A is labelled %q and %q", read.Partitions[1].Name, read.Partitions[2].Name)
	}
	if _, err := os.Stat(filepath.Join(in.esp, linuxDir)); err == nil {
		t.Error("a kernel image was laid")
	}
}

// sysfs is a /sys with what the kernel shows of a machine's block devices: files by path - a path
// ending in / is an empty directory - and links by path to where they point.
func sysfs(t *testing.T, files map[string]string, links map[string]string) string {
	t.Helper()
	sys := t.TempDir()
	for p, body := range files {
		full := filepath.Join(sys, p)
		if strings.HasSuffix(p, "/") {
			must(t, os.MkdirAll(full, 0o755))
			continue
		}
		write(t, full, body)
	}
	for p, target := range links {
		link(t, target, filepath.Join(sys, p))
	}
	return sys
}

// The disks listed are the machine's disks with their size, model and serial - never the medium, a
// CD drive, a loop device, device-mapper's, or a drive with nothing in it - and the one picked by
// itself is the first that is neither removable nor on USB: the machine's own.
func TestTheDiskPickedIsTheMachinesOwn(t *testing.T) {
	sys := sysfs(t, map[string]string{
		"devices/pci0000:00/0000:00:14.0/usb2/2-1/block/sda/size":         "61440000\n",
		"devices/pci0000:00/0000:00:14.0/usb2/2-1/block/sda/removable":    "1\n",
		"devices/pci0000:00/0000:00:1d.0/nvme/nvme0/nvme0n1/size":         "2000409264\n",
		"devices/pci0000:00/0000:00:1d.0/nvme/nvme0/nvme0n1/removable":    "0\n",
		"devices/pci0000:00/0000:00:1d.0/nvme/nvme0/nvme0n1/device/model": "Samsung SSD 990 PRO 1TB  \n",
		"devices/virtual/block/sdb/size":                                  "61440000\n",
		"devices/virtual/block/sr0/size":                                  "1064960\n",
		"devices/virtual/block/loop0/size":                                "100\n",
		"devices/virtual/block/dm-0/size":                                 "100\n",
		"devices/virtual/block/sdc/size":                                  "0\n",
	}, map[string]string{
		"block/sda":     "../devices/pci0000:00/0000:00:14.0/usb2/2-1/block/sda",
		"block/nvme0n1": "../devices/pci0000:00/0000:00:1d.0/nvme/nvme0/nvme0n1",
		"block/sdb":     "../devices/virtual/block/sdb",
		"block/sr0":     "../devices/virtual/block/sr0",
		"block/loop0":   "../devices/virtual/block/loop0",
		"block/dm-0":    "../devices/virtual/block/dm-0",
		"block/sdc":     "../devices/virtual/block/sdc",
	})
	disks, err := Disks(sys, "sdb")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range disks {
		names = append(names, d.Name)
	}
	if !slices.Equal(names, []string{"nvme0n1", "sda"}) {
		t.Fatalf("the disks are %v", names)
	}
	if !disks[1].USB || !disks[1].Removable || disks[0].USB || disks[0].Model != "Samsung SSD 990 PRO 1TB" {
		t.Errorf("the disks are %+v", disks)
	}
	d, err := Default(disks)
	if err != nil || d.Name != "nvme0n1" {
		t.Errorf("picked %+v, %v", d, err)
	}
	if _, err := Default(disks[1:]); err == nil {
		t.Error("a USB stick was picked by itself")
	}
}

// The medium is the disk under the running root's device-mapper device: a live stick's root is two
// of its partitions.
func TestTheMediumIsTheDiskUnderTheRoot(t *testing.T) {
	sys := sysfs(t, map[string]string{
		"block/dm-0/dm/name":             "root\n",
		"devices/usb/sda/sda2/partition": "2\n",
		"devices/usb/sda/sda3/partition": "3\n",
		"class/block/dm-0/slaves/sda2/":  "",
		"class/block/dm-0/slaves/sda3/":  "",
	}, map[string]string{
		"class/block/sda2": "../../devices/usb/sda/sda2",
		"class/block/sda3": "../../devices/usb/sda/sda3",
	})
	if d, err := MediumDisk(sys); err != nil || d != "sda" {
		t.Errorf("the medium is %q, %v", d, err)
	}
}

// testVarUUID is /var's UUID for the machine id of TestVarUUIDIsSystemds.
const testVarUUID = "cc7a4ffd-7252-425e-8a6a-2142f38a9264"

// The kernel image is downloaded beside its name and renamed only once its digest and what it
// names are the release's: one that is not leaves nothing on the ESP for systemd-boot to boot.
func TestAKernelImageThatIsNotTheReleasesIsNotLeftOnTheESP(t *testing.T) {
	const version = "20261013.1"
	from := t.TempDir()
	write(t, filepath.Join(from, "EFI/systemd/systemd-bootx64.efi"), "systemd-boot")
	write(t, filepath.Join(from, "loader/keys/auto/db.auth"), "db")
	cmdline := "roothash=" + strings.ReplaceAll(newRoot+newVerity, "-", "")
	image := func(v string) []byte {
		return peImage(t, map[string]string{".osrel": "IMAGE_VERSION=" + v + "\n", ".cmdline": cmdline})
	}
	good, other := image(version), image("20261013.2")
	for name, tc := range map[string]struct {
		body   []byte
		digest string
	}{
		"its own":          {good, sumOf(good)},
		"not its digest's": {good[1:], sumOf(good)},
		"another version":  {other, sumOf(other)},
	} {
		t.Run(name, func(t *testing.T) {
			r := Release{Version: version, Root: Artifact{UUID: newRoot}, Verity: Artifact{UUID: newVerity},
				UKI: Artifact{Name: ImageID + "_" + version + ".efi", SHA256: tc.digest}}
			fetch := func(context.Context, string) (io.ReadCloser, int64, error) {
				return io.NopCloser(bytes.NewReader(tc.body)), int64(len(tc.body)), nil
			}
			dir := t.TempDir()
			err := layESP(t.Context(), dir, from, r, fetch)
			left, rerr := os.ReadDir(filepath.Join(dir, linuxDir))
			must(t, rerr)
			var names []string
			for _, e := range left {
				names = append(names, e.Name())
			}
			if name == "its own" {
				if err != nil || !slices.Equal(names, []string{r.UKI.Name}) {
					t.Errorf("%v; %s holds %q", err, linuxDir, names)
				}
				return
			}
			if err == nil || len(names) != 0 {
				t.Errorf("%v; %s holds %q", err, linuxDir, names)
			}
		})
	}
}

// A stick without systemd-boot, or without the Secure Boot variables an installed disk enrolls
// from, installs no ESP: the disk would boot nothing, or boot with Secure Boot left to whatever
// keys the firmware holds.
func TestAStickWithoutSystemdBootOrItsKeysLaysNoESP(t *testing.T) {
	noKeys, noBoot := t.TempDir(), t.TempDir()
	write(t, filepath.Join(noKeys, "EFI/systemd/systemd-bootx64.efi"), "systemd-boot")
	write(t, filepath.Join(noBoot, "loader/keys/auto/db.auth"), "db")
	fetch := func(context.Context, string) (io.ReadCloser, int64, error) {
		t.Error("the kernel image was fetched")
		return nil, 0, os.ErrNotExist
	}
	for name, from := range map[string]string{"no Secure Boot variables": noKeys, "no systemd-boot": noBoot} {
		dir := t.TempDir()
		if err := layESP(t.Context(), dir, from, Release{Version: "1"}, fetch); err == nil {
			t.Errorf("%s: an ESP was laid", name)
		}
		if left, _ := os.ReadDir(dir); len(left) != 0 {
			t.Errorf("%s: the ESP holds %v", name, left)
		}
	}
}
