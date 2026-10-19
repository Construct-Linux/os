package lenovo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The X9-15p's catalog as Lenovo served it on 2026-10-19: 29 packages, each with its
// descriptor's digest.
func TestParseCatalog(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "21VV_Win11.xml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entries, err := ParseCatalog(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 29 {
		t.Fatalf("%d packages", len(entries))
	}
	want := catalogEntry{Location: "https://download.lenovo.com/pccbbs/mobiles/n4qhi16w_v2_2_.xml",
		Category: "Motherboard Devices Backplanes core chipset onboard video PCIe switches"}
	var got catalogEntry
	for _, e := range entries {
		if e.Location == want.Location {
			got = e
		}
	}
	if got.Location == "" || got.Category != want.Category || len(got.Checksum) != 64 {
		t.Errorf("%+v", got)
	}
	if _, err := ParseCatalog(strings.NewReader(`<packages><package><location>x</location><checksum type="md5">00</checksum></package></packages>`)); err == nil {
		t.Error("a catalog with md5 digests taken")
	}
}

// A descriptor names the package, the PCI devices it is for and its installer with its size and
// digest: the sensor hub's and the SoundWire audio driver's, as served.
func TestParsePackage(t *testing.T) {
	const base = "https://download.lenovo.com/pccbbs/mobiles/"
	for _, c := range []struct {
		file, id, title, version, exe string
		size                          int64
		ids                           []string
	}{
		{"n4qhi16w_v2_2_.xml", "n4qhi16w_v2", "Intel Integrated Sensor Solution - 11 (25H2 or Later)", "5.8.57.0", "n4qhi16w_v2.exe", 2195568,
			[]string{"8086:7e45", "8086:7f78", "8086:e345", "8086:a845", "8086:e445", "8086:7745"}},
		{"n4qa226w_2_.xml", "n4qa226w", "Intel SoundWire Audio Driver - 11 (25H2 or Later)", "20.43.12573.6", "n4qa226w.exe", 386552192,
			[]string{"8086:e428", "8086:e328", "8086:4d28"}},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", c.file))
		if err != nil {
			t.Fatal(err)
		}
		p, err := ParsePackage(b, base+c.file)
		if err != nil {
			t.Fatal(err)
		}
		if p.ID != c.id || p.Title != c.title || p.Version != c.version {
			t.Errorf("%s: %q %q %q", c.file, p.ID, p.Title, p.Version)
		}
		if p.Installer.Name != c.exe || p.Installer.Size != c.size || len(p.Installer.SHA256) != 64 {
			t.Errorf("%s: installer %+v", c.file, p.Installer)
		}
		if strings.Join(p.HardwareIDs, " ") != strings.Join(c.ids, " ") {
			t.Errorf("%s: ids %v", c.file, p.HardwareIDs)
		}
		if p.InstallerURL() != base+c.exe {
			t.Errorf("%s: url %s", c.file, p.InstallerURL())
		}
		// The X9-15p's sensor hub is 8086:e345, its sound device 8086:e428; a package for
		// neither is not taken.
		if !p.For([]string{"8086:e345", "8086:e428"}) || p.For([]string{"8086:64a0"}) {
			t.Errorf("%s: For", c.file)
		}
	}
}

// innoextract's listing of the audio driver's installer: 525 lines name 524 files, one of them
// twice (overwritten by a later copy).
func TestParseListing(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "n4qa226w.list"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	files := ParseListing(f)
	if len(files) != 524 {
		t.Errorf("%d files", len(files))
	}
	want := "code$GetExtractPath$/CS/XU_Ext/lenovo/tn/35L57/2355/dflt/b2_dflt_SS1_2355_LW_l1u0.bin"
	found := false
	for _, f := range files {
		found = found || f == want
	}
	if !found {
		t.Errorf("no %s", want)
	}
}
