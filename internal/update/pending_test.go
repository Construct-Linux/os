package update

import "testing"

func TestPendingIsTheNewestVersionSystemdBootWillStillTry(t *testing.T) {
	booted := "20261001.120000"
	for _, c := range []struct {
		name  string
		names []string
		want  string
	}{
		{"nothing newer", []string{ImageID + "_20261001.120000.efi", ImageID + "_20260930.120000.efi"}, ""},
		{"written, not booted yet", []string{ImageID + "_20261001.120000.efi", ImageID + "_20261002.090000+3-0.efi"}, "20261002.090000"},
		{"tried once already", []string{ImageID + "_20261002.090000+2-1.efi"}, "20261002.090000"},
		{"tries ran out: fallen back from", []string{ImageID + "_20261002.090000+0-3.efi"}, ""},
		{"the newest of two", []string{ImageID + "_20261002.090000+3.efi", ImageID + "_20261003.090000+3-0.efi"}, "20261003.090000"},
		{"blessed but not booted", []string{ImageID + "_20261002.090000.efi"}, "20261002.090000"},
		{"another OS's image", []string{"fedora_44.efi", ImageID + "_x.efi.bak"}, ""},
	} {
		if got := Pending(c.names, booted); got != c.want {
			t.Errorf("%s: Pending = %q, want %q", c.name, got, c.want)
		}
	}
}
