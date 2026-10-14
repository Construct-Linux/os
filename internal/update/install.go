package update

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/Construct-Linux/os/internal/fsx"
	"github.com/Construct-Linux/os/internal/gpt"
)

// typeVar is /var's partition type, x86-64's in the Discoverable Partitions Specification.
const typeVar = "4d21b016-b534-45c2-a9fb-5c16e091fd2d"

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

// The names of the installed disk's partitions besides the slots', for a person reading the table:
// systemd finds them by type (and /var by its UUID, VarUUID).
const (
	espName = "esp"
	varName = "var"
)

// installDisk is the table an install writes and the partitions of it the install itself writes
// or makes, each with its number in the table.
type installDisk struct {
	table                      gpt.Table
	esp, root, verity, varPart gpt.Partition
}

// layout is the table the installer writes on a disk of size bytes for r: the ESP, slot A holding
// r - labelled with its version and given the UUIDs its files name, which its kernel image's
// command line names - slot B Empty for the first update, and /var, with varUUID (VarUUID: the
// one its installation's machine id gives it). uuid makes every other GUID.
func layout(r Release, size int64, varUUID string, uuid func() string) (installDisk, error) {
	var d installDisk
	var parts []gpt.Partition
	add := func(into *gpt.Partition, p gpt.Partition) {
		// Each entry's number is its place: what the kernel calls the partition once it reads the
		// table, and the firmware's boot entry the ESP.
		p.Number = len(parts) + 1
		parts = append(parts, p)
		if into != nil {
			*into = p
		}
	}
	at := int64(firstSector)
	label := ImageID + "_" + r.Version
	for _, p := range []struct {
		into            *gpt.Partition
		typ, uuid, name string
		sectors         int64
	}{
		{&d.esp, gpt.TypeESP, uuid(), espName, espSectors},
		{&d.root, typeRoot, r.Root.UUID, label, rootSectors},
		{&d.verity, typeRootVerity, r.Verity.UUID, label, veritySectors},
		{nil, typeRoot, uuid(), Empty, rootSectors},
		{nil, typeRootVerity, uuid(), Empty, veritySectors},
	} {
		add(p.into, gpt.Partition{Type: p.typ, UUID: p.uuid, First: at, Last: at + p.sectors - 1, Name: p.name})
		at += p.sectors
	}
	last := gpt.LastUsable(size)
	if (last-at+1)*gpt.Sector < minVar {
		return installDisk{}, fmt.Errorf("install: the disk is %s, and the desktop needs %s at least",
			humanSize(size), humanSize((at+int64(minVar/gpt.Sector)+40)*gpt.Sector))
	}
	add(&d.varPart, gpt.Partition{Type: typeVar, UUID: varUUID, First: at, Last: last, Name: varName})
	d.table = gpt.Table{Disk: uuid(), Partitions: parts}
	return d, nil
}

// InstallOptions is what an install reads, writes and asks of the machine.
type InstallOptions struct {
	// Keys check the release's signature; Source is where it is, as for an update.
	Keys   openpgp.KeyRing
	Source string
	// Disk is the disk to install on, and Device it open to be read and written, which the caller
	// closes; Reread has the kernel read its new table, and Partition answers the device of its
	// partition number n once the kernel has it.
	Disk      BlockDisk
	Device    Disk
	Reread    func() error
	Partition func(n int) (string, error)
	// Run runs a program - mkfs.vfat - and MountESP mounts the new ESP, answering where and how to
	// unmount it.
	Run      func(ctx context.Context, name string, args ...string) error
	MountESP func(ctx context.Context, dev string) (string, func() error, error)
	// MakeVar is given /var's partition to make /var on: its encryption, its file system, and who
	// the machine is for are the caller's (cmd/construct).
	MakeVar func(ctx context.Context, partition string) error
	// Progress, when set, is told how far slot A's files are written: done of total bytes of the
	// file as fetched (total -1 when the source does not say), for an installer to show.
	Progress func(file string, done, total int64)
	// VarUUID is /var's partition UUID: VarUUID of the machine's id.
	VarUUID string
	// Credentials go on the new ESP, as loader/credentials/<name>.cred, for systemd-stub to hand
	// the initrd: encrypted, as systemd takes nothing else from the ESP.
	Credentials map[string][]byte
	// BootFrom is the ESP the install is made from - the stick's: its systemd-boot, signed for
	// Secure Boot, and the Secure Boot variables under loader/keys, which the new ESP is given.
	BootFrom string
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
// to - /var is made by o.MakeVar, and the firmware boots the disk first.
func Install(ctx context.Context, o InstallOptions) error {
	r, fetch, err := readSource(ctx, o.Source, o.Keys)
	if err != nil {
		return err
	}
	lay, err := layout(r, o.Disk.Size, o.VarUUID, o.UUID)
	if err != nil {
		return err
	}
	table := lay.table
	log := o.Log.With("disk", o.Disk.Path(), "version", r.Version)
	d := o.Device
	// Slot A is Empty until what it holds is checked: a copy cut off or refused leaves a disk with
	// nothing that boots, not a slot a label vouches for.
	writing := table
	writing.Partitions = slices.Clone(table.Partitions)
	for _, p := range []gpt.Partition{lay.root, lay.verity} {
		writing.Partitions[p.Number-1].Name = Empty
	}
	log.Info("writing the partition table")
	if err := gpt.Write(d, o.Disk.Size, writing); err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		return err
	}
	for _, w := range []struct {
		part gpt.Partition
		file Artifact
	}{{lay.root, r.Root}, {lay.verity, r.Verity}} {
		log.Info("writing slot A", "file", w.file.Name)
		if err := writePartition(ctx, d, w.part, w.file, fetch, o.Progress); err != nil {
			return err
		}
	}
	if err := d.Sync(); err != nil {
		return err
	}
	// Slot A is checked: the table again, now with its labels.
	if err := gpt.Write(d, o.Disk.Size, table); err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		return err
	}
	if err := o.Reread(); err != nil {
		return fmt.Errorf("install: the kernel reading the new table: %w", err)
	}
	esp, err := o.Partition(lay.esp.Number)
	if err != nil {
		return err
	}
	vardev, err := o.Partition(lay.varPart.Number)
	if err != nil {
		return err
	}
	log.Info("making the ESP")
	if err := o.Run(ctx, "mkfs.vfat", "-F", "32", "-n", "ESP", esp); err != nil {
		return err
	}
	log.Info("making /var")
	if err := o.MakeVar(ctx, vardev); err != nil {
		return fmt.Errorf("install: making /var: %w", err)
	}
	dir, unmount, err := o.MountESP(ctx, esp)
	if err != nil {
		return err
	}
	err = layESP(ctx, dir, o.BootFrom, r, fetch)
	if err == nil {
		err = writeCredentials(dir, o.Credentials)
	}
	if uerr := unmount(); err == nil {
		err = uerr
	}
	if err != nil {
		return err
	}
	n, err := bootEntry(o.Vars, lay.esp)
	if err != nil {
		return err
	}
	log.Info("installed: the firmware boots the disk first", "entry", fmt.Sprintf("Boot%04X", n))
	return nil
}

// loaderConf is systemd-boot's configuration on the installed ESP: three seconds to choose another
// version - the one to fall back to by hand - no editing of a command line, which with Secure Boot
// off would be anybody at the keyboard's root, and the Secure Boot keys offered for enrolling
// while the firmware is in setup mode (by itself in a VM).
const loaderConf = "timeout 3\neditor no\nsecure-boot-enroll if-safe\n"

// layESP puts on the ESP mounted at dir systemd-boot from the ESP at from - at systemd's path,
// which the boot entry starts, and the removable one, which firmware boots without an entry - its
// configuration, from's Secure Boot variables, and r's kernel image, blessed, held to its digest
// and to naming slot A.
func layESP(ctx context.Context, dir, from string, r Release, fetch Fetch) error {
	boot, err := os.ReadFile(filepath.Join(from, "EFI/systemd/systemd-bootx64.efi"))
	if err != nil {
		return fmt.Errorf("install: the stick's systemd-boot: %w", err)
	}
	keys, err := filepath.Glob(filepath.Join(from, "loader/keys/*/*.auth"))
	if err != nil || len(keys) == 0 {
		return fmt.Errorf("install: the stick carries no Secure Boot variables (loader/keys): %v", err)
	}
	for _, k := range keys {
		b, err := os.ReadFile(k)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, k)
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			return err
		}
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
	linux := filepath.Join(dir, linuxDir)
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
	return fsx.SyncDir(linux)
}

// writeCredentials puts creds on the ESP mounted at dir, where systemd-stub reads the ones every
// kernel image is given.
func writeCredentials(dir string, creds map[string][]byte) error {
	d := filepath.Join(dir, "loader", "credentials")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	for name, b := range creds {
		if err := os.WriteFile(filepath.Join(d, name+".cred"), b, 0o644); err != nil {
			return err
		}
	}
	return fsx.SyncDir(d)
}

// RandomUUID is a random (version 4) UUID, as a GPT's GUIDs are made.
func RandomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand never fails on Linux
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return formatUUID(hex.EncodeToString(b[:]))
}
