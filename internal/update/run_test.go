package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Slot B's first bytes, and slot A's, where testLayout puts them.
const (
	slotAOffset = 9 << 20
	slotBOffset = 14 << 20
	verityB     = 18 << 20
)

// The UUIDs of the slot a test release is for.
const (
	newRoot   = "0123abcd-0000-1111-2222-333344445555"
	newVerity = "89abcdef-6666-7777-8888-9999aaaabbbb"
)

func sumOf(b []byte) string {
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}

// An update writes the release into the slot the machine did not boot - its root and hash tree as
// released, labelled with the version and given the UUIDs the release names - and its kernel image
// beside the booted one's with three tries; slot A and its image are as they were; nothing reboots,
// and the result says what a reboot moves to.
func TestAnUpdateWritesTheSlotTheMachineDidNotBoot(t *testing.T) {
	m := testMachine(t)
	dir, body := release(t, "1.1", newRoot, newVerity, signer(), 0xb)
	slotA := m.at(t, slotAOffset, rootSize)
	res, err := Run(t.Context(), m.options(t, dir))
	if err != nil {
		t.Fatalf("%v\n%s", err, m.log.String())
	}
	if res != (Result{Written: "1.1", Pending: "1.1"}) {
		t.Errorf("the result is %+v", res)
	}
	tb := m.table(t)
	if p := tb.Partitions[3]; p.UUID != newRoot || p.Name != ImageID+"_1.1" {
		t.Errorf("slot B's root is %+v", p)
	}
	if p := tb.Partitions[4]; p.UUID != newVerity || p.Name != ImageID+"_1.1" {
		t.Errorf("slot B's hash tree is %+v", p)
	}
	if p := tb.Partitions[1]; p.UUID != rootA || p.Name != ImageID+"_1.0" {
		t.Errorf("slot A's root is %+v", p)
	}
	if !bytes.Equal(m.at(t, slotBOffset, int64(len(body))), body) {
		t.Error("slot B does not hold the release's root")
	}
	if !bytes.Equal(m.at(t, verityB, 64<<10), bytes.Repeat([]byte{0xc}, 64<<10)) {
		t.Error("slot B does not hold the release's hash tree")
	}
	if !bytes.Equal(m.at(t, slotAOffset, rootSize), slotA) {
		t.Error("slot A was written")
	}
	if got := m.images(t); !slices.Equal(got, []string{ImageID + "_1.0.efi", ImageID + "_1.1+3.efi"}) {
		t.Errorf("the ESP holds %v", got)
	}
}

// The next update, once the machine runs 1.1 from slot B, is written into slot A - the older one -
// and 1.0's kernel image is gone: two versions at most, and never the booted one touched.
func TestTheNextUpdateTakesTheOlderSlot(t *testing.T) {
	m := testMachine(t)
	dir, _ := release(t, "1.1", newRoot, newVerity, signer(), 0xb)
	if _, err := Run(t.Context(), m.options(t, dir)); err != nil {
		t.Fatalf("%v\n%s", err, m.log.String())
	}
	// It reboots into slot B, and the boot is blessed.
	m.boot(t, "1.1", slotBOffset/512, verityB/512)
	must(t, os.Rename(filepath.Join(m.esp, Linux, ImageID+"_1.1+3.efi"), filepath.Join(m.esp, Linux, ImageID+"_1.1.efi")))
	// And a kernel image of a version no slot holds, which the ESP has no room to keep.
	write(t, filepath.Join(m.esp, Linux, ImageID+"_0.9+0-3.efi"), "stale")
	slotB := m.at(t, slotBOffset, rootSize)
	const root2, verity2 = "fedc0000-1111-2222-3333-444455556666", "0000fedc-7777-8888-9999-aaaabbbbcccc"
	dir, body := release(t, "1.2", root2, verity2, signer(), 0xd)
	if _, err := Run(t.Context(), m.options(t, dir)); err != nil {
		t.Fatalf("%v\n%s", err, m.log.String())
	}
	tb := m.table(t)
	if p := tb.Partitions[1]; p.UUID != root2 || p.Name != ImageID+"_1.2" {
		t.Errorf("slot A's root is %+v", p)
	}
	if !bytes.Equal(m.at(t, slotAOffset, int64(len(body))), body) {
		t.Error("slot A does not hold 1.2's root")
	}
	if !bytes.Equal(m.at(t, slotBOffset, rootSize), slotB) || tb.Partitions[3].Name != ImageID+"_1.1" {
		t.Error("the booted slot was written")
	}
	if got := m.images(t); !slices.Equal(got, []string{ImageID + "_1.1.efi", ImageID + "_1.2+3.efi"}) {
		t.Errorf("the ESP holds %v", got)
	}
}

// What an update refuses it refuses before it writes anything: a release older than the machine
// runs, however well signed - an old stick plugged in - and one signed by another key. The disk
// and the ESP are as they were.
func TestAnUpdateRefusesADowngradeAndAnotherKeysRelease(t *testing.T) {
	for name, tc := range map[string]struct {
		version string
		key     bool
		fails   string
	}{
		"an older release":      {version: "0.9", key: true, fails: "older"},
		"a release signed else": {version: "1.1", key: false, fails: "not signed by the key"},
	} {
		t.Run(name, func(t *testing.T) {
			m := testMachine(t)
			k := signer()
			if !tc.key {
				k = otherKey()
			}
			dir, _ := release(t, tc.version, newRoot, newVerity, k, 0xb)
			before := m.at(t, 0, diskSize)
			_, err := Run(t.Context(), m.options(t, dir))
			if err == nil || !strings.Contains(err.Error(), tc.fails) {
				t.Fatalf("err = %v, want one saying %q", err, tc.fails)
			}
			if tc.version == "0.9" && !errors.Is(err, ErrOlder) {
				t.Errorf("a downgrade was %v, not ErrOlder", err)
			}
			if !bytes.Equal(m.at(t, 0, diskSize), before) {
				t.Error("the disk was written")
			}
			if got := m.images(t); !slices.Equal(got, []string{ImageID + "_1.0.efi"}) {
				t.Errorf("the ESP holds %v", got)
			}
		})
	}
}

// A stick may carry a directory of releases: the newest is written. One of them signed by another
// key refuses the stick whole, newest or not.
func TestTheNewestReleaseOnTheStickIsTheOneWritten(t *testing.T) {
	m := testMachine(t)
	stick := t.TempDir()
	for _, r := range []struct {
		version, root, verity string
	}{{"1.1", newRoot, newVerity}, {"1.2", "fedc0000-1111-2222-3333-444455556666", "0000fedc-7777-8888-9999-aaaabbbbcccc"}} {
		dir, _ := release(t, r.version, r.root, r.verity, signer(), 0xb)
		must(t, os.Rename(dir, filepath.Join(stick, r.version)))
	}
	res, err := Run(t.Context(), m.options(t, stick))
	if err != nil || res.Written != "1.2" {
		t.Fatalf("wrote %+v, %v\n%s", res, err, m.log.String())
	}

	m = testMachine(t)
	dir, _ := release(t, "1.3", newRoot, newVerity, otherKey(), 0xb)
	must(t, os.Rename(dir, filepath.Join(stick, "1.3")))
	before := m.at(t, 0, diskSize)
	if _, err := Run(t.Context(), m.options(t, stick)); err == nil {
		t.Fatal("a stick carrying a release of another key was taken")
	}
	if !bytes.Equal(m.at(t, 0, diskSize), before) {
		t.Error("the disk was written")
	}
}

// A source that holds no release is an error, not a quiet nothing: the stick plugged in is not
// what the person thinks it is.
func TestASourceWithNoReleaseIsAnError(t *testing.T) {
	m := testMachine(t)
	if _, err := Run(t.Context(), m.options(t, t.TempDir())); err == nil {
		t.Error("an empty source was taken")
	}
}

// A root that is not what the list says is a slot left empty and no kernel image for it: nothing
// boots it. So is a kernel image that boots another slot than the one written.
func TestAFileThatIsNotTheReleasesLeavesTheSlotEmpty(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, dir string){
		"a root changed after it was listed": func(t *testing.T, dir string) {
			roots, _ := filepath.Glob(filepath.Join(dir, "*.root.raw"))
			f, err := os.OpenFile(roots[0], os.O_WRONLY, 0)
			must(t, err)
			_, err = f.WriteAt([]byte{0}, 100)
			must(t, err)
			must(t, f.Close())
		},
		"a kernel image changed after it was listed": func(t *testing.T, dir string) {
			ukis, _ := filepath.Glob(filepath.Join(dir, "*.efi"))
			f, err := os.OpenFile(ukis[0], os.O_WRONLY|os.O_APPEND, 0)
			must(t, err)
			_, err = f.Write([]byte{0})
			must(t, err)
			must(t, f.Close())
		},
		"a kernel image of another slot": func(t *testing.T, dir string) {
			ukis, _ := filepath.Glob(filepath.Join(dir, "*.efi"))
			write(t, ukis[0], string(peImage(t, map[string]string{".osrel": "IMAGE_VERSION=1.1\n", ".cmdline": "PARTUUID=" + rootA + " PARTUUID=" + verityA + " "})))
			seal(t, dir, signer())
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := testMachine(t)
			dir, _ := release(t, "1.1", newRoot, newVerity, signer(), 0xb)
			change(t, dir)
			if _, err := Run(t.Context(), m.options(t, dir)); err == nil {
				t.Fatal("the release was written")
			}
			if got := m.images(t); !slices.Equal(got, []string{ImageID + "_1.0.efi"}) {
				t.Errorf("the ESP holds %v", got)
			}
			if tb := m.table(t); tb.Partitions[3].Name != Empty && tb.Partitions[3].Name != ImageID+"_1.1" {
				t.Errorf("slot B is labelled %s", tb.Partitions[3].Name)
			}
		})
	}
}

// What is copied from a release is held to its digest and to the room it has: a file of exactly
// that room is written whole, and one a byte longer is not.
func TestAFileIsHeldToItsRoom(t *testing.T) {
	body := bytes.Repeat([]byte{7}, 4096)
	a := Artifact{Name: "root", SHA256: sumOf(body)}
	var w bytes.Buffer
	if err := copyChecked(&w, bytes.NewReader(body), int64(len(body)), a); err != nil || !bytes.Equal(w.Bytes(), body) {
		t.Errorf("a file of exactly its room: %v, %d bytes written", err, w.Len())
	}
	longer := append(bytes.Clone(body), 7)
	if err := copyChecked(io.Discard, bytes.NewReader(longer), int64(len(body)), Artifact{Name: "root", SHA256: sumOf(longer)}); err == nil {
		t.Error("a file a byte longer than its room was taken")
	}
	if err := copyChecked(io.Discard, bytes.NewReader(body[1:]), int64(len(body)), a); err == nil {
		t.Error("a file that is not its digest's was taken")
	}
}

// What is read before its signature is checked is read whole up to its bound, and refused past it.
func TestTheListIsReadUpToItsBound(t *testing.T) {
	fetch := func(body []byte) Fetch {
		return func(context.Context, string) (io.ReadCloser, int64, error) {
			return io.NopCloser(bytes.NewReader(body)), -1, nil
		}
	}
	whole := bytes.Repeat([]byte{'x'}, 100)
	if got, err := readSmall(t.Context(), fetch(whole), SumsFile, 100); err != nil || !bytes.Equal(got, whole) {
		t.Errorf("a list of its bound read as %d bytes, %v", len(got), err)
	}
	if got, err := readSmall(t.Context(), fetch(append(whole, 'x')), SumsFile, 100); err == nil {
		t.Errorf("a list past its bound read as %d bytes", len(got))
	}
}

// A release the machine runs is nothing to do, and so is one it wrote already - it is not written
// again, which would give a kernel image that failed three more tries - and a version written with
// tries left is what a reboot moves to; one whose tries ran out is not.
func TestAWrittenReleaseIsNotWrittenAgain(t *testing.T) {
	m := testMachine(t)
	dir, _ := release(t, "1.0", rootA, verityA, signer(), 0xb)
	if res, err := Run(t.Context(), m.options(t, dir)); err != nil || res != (Result{}) {
		t.Fatalf("the release it runs: %+v, %v", res, err)
	}

	dir, _ = release(t, "1.1", newRoot, newVerity, signer(), 0xb)
	if _, err := Run(t.Context(), m.options(t, dir)); err != nil {
		t.Fatal(err)
	}
	written := m.at(t, 0, diskSize)
	res, err := Run(t.Context(), m.options(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(m.at(t, 0, diskSize), written) || res != (Result{Pending: "1.1"}) {
		t.Errorf("a release written already: %+v", res)
	}
	// Its three tries ran out: the machine fell back, and stays.
	must(t, os.Rename(filepath.Join(m.esp, Linux, ImageID+"_1.1+3.efi"), filepath.Join(m.esp, Linux, ImageID+"_1.1+0-3.efi")))
	if res, err = Run(t.Context(), m.options(t, dir)); err != nil || res.Pending != "" {
		t.Errorf("a version whose tries ran out is pending: %+v, %v", res, err)
	}
}
