package update

import "testing"

// The vector is systemd's own: systemd-id128 machine-id --app-specific=<var's type> --uuid, on a
// machine whose id is the one given.
func TestVarUUIDIsSystemds(t *testing.T) {
	got, err := VarUUID("2125b34c411146e6844da0b4c60315bd\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := "cc7a4ffd-7252-425e-8a6a-2142f38a9264"; got != want {
		t.Fatalf("VarUUID = %s, want %s", got, want)
	}
	if _, err := VarUUID("not hex"); err == nil {
		t.Fatal("a malformed machine id was taken")
	}
}
