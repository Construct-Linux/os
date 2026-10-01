package update

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/spin-stack/spindrift/internal/gpt"
)

// TypeVar is /var's partition type, x86-64's in the Discoverable Partitions Specification.
const TypeVar = "4d21b016-b534-45c2-a9fb-5c16e091fd2d"

// The installed disk's partitions, in sectors: an ESP with room for two kernel images and many
// times their size, two slots each with room for a root several times today's (2 GiB) and its
// hash tree, and /var with the rest - where everything a person keeps lives.
const (
	espSectors    = 1 << 30 / gpt.Sector
	rootSectors   = 8 << 30 / gpt.Sector
	veritySectors = 128 << 20 / gpt.Sector
	// minVar is the least /var an install leaves room for: a disk smaller is refused.
	minVar = 8 << 30
	// firstSector aligns the first partition to a mebibyte, as every partitioning tool does.
	firstSector = 1 << 20 / gpt.Sector
)

// The names of the installed disk's partitions besides the slots': spin-init mounts the one named
// var (cmd/spin-init), and a person reading the table sees which is the ESP.
const (
	ESPName = "esp"
	VarName = "var"
)

// Layout is the table the installer writes on a disk of size bytes for r: the ESP, slot A holding
// r - labelled with its version and given the UUIDs its files name, which its kernel image's
// command line names - slot B Empty for the first update, and /var. uuid makes every other GUID.
func Layout(r Release, size int64, uuid func() string) (gpt.Table, error) {
	at := int64(firstSector)
	next := func(sectors int64) (int64, int64) {
		first := at
		at += sectors
		return first, at - 1
	}
	label := ImageID + "_" + r.Version
	var parts []gpt.Partition
	for _, p := range []struct {
		typ, uuid, name string
		sectors         int64
	}{
		{gpt.TypeESP, uuid(), ESPName, espSectors},
		{TypeRoot, r.Root.UUID, label, rootSectors},
		{TypeRootVerity, r.Verity.UUID, label, veritySectors},
		{TypeRoot, uuid(), Empty, rootSectors},
		{TypeRootVerity, uuid(), Empty, veritySectors},
	} {
		first, last := next(p.sectors)
		parts = append(parts, gpt.Partition{Type: p.typ, UUID: p.uuid, First: first, Last: last, Name: p.name})
	}
	last := gpt.LastUsable(size)
	if (last-at+1)*gpt.Sector < minVar {
		return gpt.Table{}, fmt.Errorf("install: the disk is %s, and the desktop needs %s at least",
			humanSize(size), humanSize((at+int64(minVar/gpt.Sector)+40)*gpt.Sector))
	}
	parts = append(parts, gpt.Partition{Type: TypeVar, UUID: uuid(), First: at, Last: last, Name: VarName})
	// Each entry's number is its place: what the kernel calls the partition once it reads the table.
	for i := range parts {
		parts[i].Number = i + 1
	}
	return gpt.Table{Disk: uuid(), Partitions: parts}, nil
}

// InstallOptions is what an install reads, writes and asks of the machine.
type InstallOptions struct {
	// Keys check the release's signature; Source is where it is, as for an update.
	Keys   openpgp.KeyRing
	Source string
	// Disk is the disk to install on, which Open opens; Reread has the kernel read its new table,
	// and Partition answers the device of its partition number n once the kernel has it.
	Disk      BlockDisk
	Open      func(path string) (Disk, error)
	Reread    func(d Disk) error
	Partition func(n int) (string, error)
	// Run runs a program - mkfs.vfat, mkfs.ext4 - and MountESP mounts the new ESP, answering where
	// and how to unmount it.
	Run      func(ctx context.Context, name string, args ...string) error
	MountESP func(ctx context.Context, dev string) (string, func() error, error)
	// Loader is the running root's systemd-boot, which the ESP is given.
	Loader string
	// Vars are the firmware's variables, where the boot entry is written.
	Vars Vars
	UUID func() string
	Log  *slog.Logger
}

// Install lays the desktop on o.Disk: everything on it is gone. The release under o.Source -
// checked against o.Keys before anything is written - goes into slot A, decompressed and held to
// its digests, labelled with its version only once both are; slot B is left Empty for
// systemd-sysupdate's first update, the ESP gets systemd-boot and the
// release's kernel image, blessed - a fresh install is known good, there is nothing to fall back
// to - /var is made empty, and the firmware boots the disk first. It answers the table written.
func Install(ctx context.Context, o InstallOptions) (gpt.Table, error) {
	r, fetch, err := Newest(ctx, o.Source, o.Keys)
	if err != nil {
		return gpt.Table{}, err
	}
	table, err := Layout(r, o.Disk.Size, o.UUID)
	if err != nil {
		return gpt.Table{}, err
	}
	log := o.Log.With("disk", o.Disk.Path(), "version", r.Version)
	d, err := o.Open(o.Disk.Path())
	if err != nil {
		return gpt.Table{}, err
	}
	if closer, ok := d.(io.Closer); ok {
		defer func() { _ = closer.Close() }() // every write is synced where it is made
	}
	// Slot A is Empty until what it holds is checked: a copy cut off or refused leaves a disk with
	// nothing that boots, not a slot a label vouches for.
	writing := table
	writing.Partitions = slices.Clone(table.Partitions)
	for _, i := range []int{1, 2} {
		writing.Partitions[i].Name = Empty
	}
	log.Info("writing the partition table")
	if err := gpt.Write(d, o.Disk.Size, writing); err != nil {
		return gpt.Table{}, err
	}
	if err := d.Sync(); err != nil {
		return gpt.Table{}, err
	}
	for _, w := range []struct {
		part gpt.Partition
		file Artifact
	}{{table.Partitions[1], r.Root}, {table.Partitions[2], r.Verity}} {
		log.Info("writing slot A", "file", w.file.Name)
		if err := writePartition(ctx, d, w.part, w.file, fetch); err != nil {
			return gpt.Table{}, err
		}
	}
	if err := d.Sync(); err != nil {
		return gpt.Table{}, err
	}
	for _, p := range table.Partitions[1:3] {
		if err := gpt.Rename(d, p.UUID, p.UUID, p.Name); err != nil {
			return gpt.Table{}, err
		}
	}
	if err := d.Sync(); err != nil {
		return gpt.Table{}, err
	}
	if err := o.Reread(d); err != nil {
		return gpt.Table{}, fmt.Errorf("install: the kernel reading the new table: %w", err)
	}
	esp, err := o.Partition(1)
	if err != nil {
		return gpt.Table{}, err
	}
	vardev, err := o.Partition(6)
	if err != nil {
		return gpt.Table{}, err
	}
	log.Info("making the ESP and /var")
	if err := o.Run(ctx, "mkfs.vfat", "-F", "32", "-n", "ESP", esp); err != nil {
		return gpt.Table{}, err
	}
	if err := o.Run(ctx, "mkfs.ext4", "-q", "-F", "-L", VarName, vardev); err != nil {
		return gpt.Table{}, err
	}
	dir, unmount, err := o.MountESP(ctx, esp)
	if err != nil {
		return gpt.Table{}, err
	}
	err = layESP(ctx, dir, o.Loader, r, fetch)
	if uerr := unmount(); err == nil {
		err = uerr
	}
	if err != nil {
		return gpt.Table{}, err
	}
	n, err := BootEntry(o.Vars, table, table.Partitions[0])
	if err != nil {
		return gpt.Table{}, err
	}
	log.Info("installed: the firmware boots the disk first", "entry", fmt.Sprintf("Boot%04X", n))
	return table, nil
}

// loaderConf is systemd-boot's configuration on the installed ESP: three seconds to choose another
// version - the one to fall back to by hand - and no editing of a command line, which with Secure
// Boot off would be anybody at the keyboard's root.
const loaderConf = "timeout 3\neditor no\n"

// layESP puts on the ESP mounted at dir systemd-boot - at systemd's path, which the boot entry
// starts, and the removable one, which firmware boots without an entry - its configuration, and
// r's kernel image, blessed, held to its digest and to naming slot A.
func layESP(ctx context.Context, dir, loader string, r Release, fetch Fetch) error {
	boot, err := os.ReadFile(loader)
	if err != nil {
		return fmt.Errorf("install: the running root's systemd-boot: %w", err)
	}
	for p, body := range map[string][]byte{
		"EFI/systemd/systemd-bootx64.efi": boot,
		"EFI/BOOT/BOOTX64.EFI":            boot,
		"loader/loader.conf":              []byte(loaderConf),
	} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, body, 0o644); err != nil {
			return err
		}
	}
	linux := filepath.Join(dir, Linux)
	if err := os.MkdirAll(linux, 0o755); err != nil {
		return err
	}
	partial := filepath.Join(linux, "."+r.UKI.Name+".partial")
	if err := download(ctx, fetch, r.UKI, partial); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if err := checkImage(partial, r.Version, r.Root.UUID, r.Verity.UUID); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if err := os.Rename(partial, filepath.Join(linux, ImageID+"_"+r.Version+".efi")); err != nil {
		return err
	}
	return syncDir(linux)
}

// RandomUUID is a random (version 4) UUID, as a GPT's GUIDs are made.
func RandomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand never fails on Linux
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
