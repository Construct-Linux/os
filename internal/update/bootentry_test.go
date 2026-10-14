package update

import (
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"github.com/Construct-Linux/os/internal/gpt"
)

// installed is the table of a disk the installer laid out, its ESP first.
func installed() (gpt.Table, gpt.Partition) {
	esp := gpt.Partition{Type: gpt.TypeESP, UUID: "e81bff23-fae0-4d2e-b30b-ac31735f53ab", First: 2048, Last: 2048 + 0x100000 - 1, Name: "esp", Number: 1}
	return gpt.Table{Partitions: []gpt.Partition{esp, {Type: typeRoot, UUID: "00000000-0000-0000-0000-000000000002", First: 0x100800, Last: 0x200000, Name: "root", Number: 2}}}, esp
}

func bootOrder(t *testing.T, vars Vars) []uint16 {
	t.Helper()
	raw, err := vars.Read("BootOrder", Global)
	must(t, err)
	var o []uint16
	for i := 0; i+1 < len(raw); i += 2 {
		o = append(o, binary.LittleEndian.Uint16(raw[i:]))
	}
	return o
}

// The boot entry is at the first number no entry has, active, titled, first in BootOrder before
// what was there; installing again replaces it rather than adding another.
func TestTheBootEntryStartsTheInstalledSystemdBootFirst(t *testing.T) {
	vars := Vars(t.TempDir())
	for _, n := range []string{"Boot0000", "Boot0001"} {
		must(t, vars.Write(n, Global, []byte("the firmware's")))
	}
	must(t, vars.Write("BootOrder", Global, []byte{1, 0, 0, 0}))
	_, esp := installed()
	n, err := bootEntry(vars, esp)
	if err != nil || n != 2 {
		t.Fatalf("the entry is Boot%04X, %v", n, err)
	}
	raw, err := vars.Read("Boot0002", Global)
	must(t, err)
	title := efiString(entryTitle)
	if binary.LittleEndian.Uint32(raw) != 1 || string(raw[6:6+len(title)]) != string(title) {
		t.Errorf("the entry is %x", raw)
	}
	if got := bootOrder(t, vars); len(got) != 3 || got[0] != 2 || got[1] != 1 || got[2] != 0 {
		t.Errorf("BootOrder is %v", got)
	}
	again, err := bootEntry(vars, esp)
	if err != nil || again != 2 {
		t.Errorf("installing again made Boot%04X, %v", again, err)
	}
	if entries, _ := os.ReadDir(string(vars)); len(entries) != 4 {
		t.Errorf("installing again left %d variables", len(entries))
	}
}

// The device path is nodes a firmware walks by their lengths - the hard drive's 42 bytes, the
// file's its name's, the end's four - and the path list's length is theirs together.
func TestTheBootEntrysPathIsWalkedByItsLengths(t *testing.T) {
	_, esp := installed()
	raw, err := loadOption(1, esp)
	must(t, err)
	le := binary.LittleEndian
	listLen := int(le.Uint16(raw[4:]))
	path := raw[6+len(efiString(entryTitle)):]
	if len(path) != listLen {
		t.Fatalf("the path list is %d bytes and says %d", len(path), listLen)
	}
	var nodes [][3]int
	for len(path) >= 4 {
		n := int(le.Uint16(path[2:]))
		nodes = append(nodes, [3]int{int(path[0]), int(path[1]), n})
		if n < 4 || n > len(path) {
			t.Fatalf("a node of %d bytes in %d left", n, len(path))
		}
		path = path[n:]
	}
	want := [][3]int{{4, 1, 42}, {4, 4, 4 + len(efiString(loaderPath))}, {0x7f, 0xff, 4}}
	if len(path) != 0 || fmt.Sprint(nodes) != fmt.Sprint(want) {
		t.Errorf("the path's nodes are %v and %d bytes over, want %v", nodes, len(path), want)
	}
}

// An entry's number is read as the hex it is named in: with Boot0000 to Boot0010 taken, the next is
// Boot0011.
func TestABootEntryTakesNoOneElsesNumber(t *testing.T) {
	vars := Vars(t.TempDir())
	for n := range 17 {
		must(t, vars.Write(fmt.Sprintf("Boot%04X", n), Global, []byte("the firmware's")))
	}
	_, esp := installed()
	if n, err := bootEntry(vars, esp); err != nil || n != 0x11 {
		t.Errorf("the entry is Boot%04X, %v", n, err)
	}
}

// The entry names the ESP by the number its table gives it, wherever the ESP is in the table.
func TestTheBootEntryNamesTheESPsNumber(t *testing.T) {
	vars := Vars(t.TempDir())
	_, esp := installed()
	esp.Number = 3
	n, err := bootEntry(vars, esp)
	must(t, err)
	raw, err := vars.Read(fmt.Sprintf("Boot%04X", n), Global)
	must(t, err)
	// The hard drive node, first in the path: type, subtype and length, then the partition number.
	node := raw[6+len(efiString(entryTitle)):]
	if got := binary.LittleEndian.Uint32(node[4:]); got != 3 {
		t.Errorf("the entry boots partition %d, not the ESP's 3", got)
	}
}

// Only a disk's own ESP is booted: a partition no table numbered, or one that is no ESP, is refused.
func TestOnlyTheDisksESPIsBooted(t *testing.T) {
	table, esp := installed()
	vars := Vars(t.TempDir())
	unnumbered := esp
	unnumbered.Number = 0
	if _, err := bootEntry(vars, unnumbered); err == nil {
		t.Error("an ESP no table numbered was booted")
	}
	if _, err := bootEntry(vars, table.Partitions[1]); err == nil {
		t.Error("a root partition was booted as an ESP")
	}
	if got, err := vars.Read("BootOrder", Global); err == nil {
		t.Errorf("a refused entry wrote BootOrder %v", got)
	}
}
