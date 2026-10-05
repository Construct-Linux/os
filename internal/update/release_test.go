package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// testKeys are two OpenPGP keys, made once: a release's signer, and another.
var testKeys = sync.OnceValue(func() [2]*openpgp.Entity {
	var keys [2]*openpgp.Entity
	for i := range keys {
		e, err := openpgp.NewEntity("update test", "", "test@construct", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
		if err != nil {
			panic(err)
		}
		keys[i] = e
	}
	return keys
})

// sign is key's detached binary signature of sums, as gpg --detach-sign writes it.
func sign(t *testing.T, key *openpgp.Entity, sums []byte) []byte {
	t.Helper()
	var sig bytes.Buffer
	must(t, openpgp.DetachSign(&sig, key, bytes.NewReader(sums), nil))
	return sig.Bytes()
}

// publicKeyring is key's public half as gpg --export writes it, armored or not.
func publicKeyring(t *testing.T, key *openpgp.Entity, armored bool) []byte {
	t.Helper()
	var b bytes.Buffer
	if armored {
		w, err := armor.Encode(&b, openpgp.PublicKeyType, nil)
		must(t, err)
		must(t, key.Serialize(w))
		must(t, w.Close())
		return b.Bytes()
	}
	must(t, key.Serialize(&b))
	return b.Bytes()
}

// trusted is the keyring a test machine's root carries: the signer's public key.
func trusted(t *testing.T) openpgp.EntityList {
	t.Helper()
	keys, err := ParseKeyring(publicKeyring(t, testKeys()[0], true))
	must(t, err)
	return keys
}

// A release's list is taken only signed by the key the machine trusts, and only as signed: another
// key's signature and a list changed after it was signed are each refused; an armored signature is
// as good as a binary one.
func TestAReleaseIsTakenOnlyAsItsKeySignedIt(t *testing.T) {
	keys := testKeys()
	sums := []byte(strings.Repeat("0", 64) + "  " + ImageID + "_1.efi\n")
	sig := sign(t, keys[0], sums)
	if err := Verify(trusted(t), sums, sig); err != nil {
		t.Fatalf("the release's own signature was refused: %v", err)
	}
	var armored bytes.Buffer
	must(t, openpgp.ArmoredDetachSign(&armored, keys[0], bytes.NewReader(sums), nil))
	if err := Verify(trusted(t), sums, armored.Bytes()); err != nil {
		t.Errorf("an armored signature was refused: %v", err)
	}
	if err := Verify(trusted(t), sums, sign(t, keys[1], sums)); err == nil {
		t.Error("a release signed by another key was taken")
	}
	changed := append([]byte(nil), sums...)
	changed[0] = '1'
	if err := Verify(trusted(t), changed, sig); err == nil {
		t.Error("a list changed after it was signed was taken")
	}
}

// The keyring a root carries is read armored or binary, and one with no key is refused.
func TestTheKeyringIsReadEitherWay(t *testing.T) {
	for _, armored := range []bool{true, false} {
		keys, err := ParseKeyring(publicKeyring(t, testKeys()[0], armored))
		if err != nil || len(keys) != 1 {
			t.Errorf("armored %v: %d keys, %v", armored, len(keys), err)
		}
	}
	if _, err := ParseKeyring([]byte("not a key")); err == nil {
		t.Error("a keyring of no key was taken")
	}
}

// Writing the list lists every file in the directory but the list and its signature, and the list
// it writes is one ParseSums reads back.
func TestWritingTheListListsTheRelease(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{"a.raw": "root", "b.efi": "kernel"}
	for name, body := range files {
		write(t, filepath.Join(dir, name), body)
	}
	// A list and a signature left from before are not listed in the new one.
	write(t, filepath.Join(dir, SumsFile), "stale")
	write(t, filepath.Join(dir, SignatureFile), "stale")
	must(t, WriteSums(dir))
	raw, err := os.ReadFile(filepath.Join(dir, SumsFile))
	must(t, err)
	sums, err := ParseSums(raw)
	must(t, err)
	if len(sums) != len(files) {
		t.Errorf("the list is %v", sums)
	}
	for name, body := range files {
		d := sha256.Sum256([]byte(body))
		if sums[name] != hex.EncodeToString(d[:]) {
			t.Errorf("%s is listed as %q", name, sums[name])
		}
	}
	must(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	if err := WriteSums(dir); err == nil {
		t.Error("a directory holding a directory was listed")
	}
}

// SHA256SUMS is read one way: a digest, two spaces or a space and a star, a name with no
// directory; a name twice, or any other line, refuses the list.
func TestTheListIsReadOneWay(t *testing.T) {
	d := strings.Repeat("a", 64)
	sums, err := ParseSums([]byte(d + "  one\n" + d + " *two\n"))
	if err != nil || len(sums) != 2 || sums["one"] != d || sums["two"] != d {
		t.Fatalf("a good list read as %v, %v", sums, err)
	}
	for name, raw := range map[string]string{
		"a name twice":        d + "  one\n" + d + "  one\n",
		"a directory":         d + "  ../one\n",
		"a short digest":      d[1:] + "  one\n",
		"an uppercase digest": strings.ToUpper(d) + "  one\n",
		"one space":           d + " one\n",
		"no name":             d + "  \n",
		"a blank line":        d + "  one\n\n",
	} {
		if got, err := ParseSums([]byte(raw)); err == nil {
			t.Errorf("a list with %s read as %v", name, got)
		}
	}
}

// A release is a root, a hash tree and a kernel image of one version - the version cmd/image usb
// writes, a UTC time - named as Names names them, the UUIDs their names give the slot's partitions
// read back; anything else beside them is none of a machine's business. One missing, two of one,
// two versions, or one partition for both, is no release.
func TestAReleaseIsThreeFilesOfOneVersion(t *testing.T) {
	const root, verity = "a201682a-bb1c-9d0b-7b31-5d957af007b9", "3f3c0a48-7b27-5b3b-73c2-385422d45830"
	const v1, v2 = "20261001.134321", "20261002.090000"
	list := func(version string) map[string]string {
		r, v, u := Names(version, root, verity)
		return map[string]string{r: "1" + strings.Repeat("0", 63), v: "2" + strings.Repeat("0", 63), u: "3" + strings.Repeat("0", 63),
			"README": strings.Repeat("4", 64)}
	}
	got, err := Find(list(v1))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != v1 || got.Root.UUID != root || got.Verity.UUID != verity || got.UKI.UUID != "" ||
		got.Root.Name != ImageID+"_"+v1+"_"+root+".root.raw.xz" || got.UKI.Name != ImageID+"_"+v1+".efi" ||
		!strings.HasPrefix(got.Verity.SHA256, "2") {
		t.Errorf("the release is %+v", got)
	}
	for name, change := range map[string]func(map[string]string){
		"no root": func(m map[string]string) {
			r, _, _ := Names(v1, root, verity)
			delete(m, r)
		},
		"no kernel image":   func(m map[string]string) { delete(m, ImageID+"_"+v1+".efi") },
		"two kernel images": func(m map[string]string) { m[ImageID+"_"+v2+".efi"] = strings.Repeat("5", 64) },
		"two versions": func(m map[string]string) {
			delete(m, ImageID+"_"+v1+".efi")
			m[ImageID+"_"+v2+".efi"] = strings.Repeat("5", 64)
		},
		"one partition for both": func(m map[string]string) {
			_, v, _ := Names(v1, root, verity)
			delete(m, v)
			_, v, _ = Names(v1, root, root)
			m[v] = strings.Repeat("6", 64)
		},
	} {
		m := list(v1)
		change(m)
		if got, err := Find(m); err == nil {
			t.Errorf("a list with %s was the release %+v", name, got)
		}
	}
}

func TestAKeyringWithAKeyThatIsNotEd25519IsRefused(t *testing.T) {
	rsaKey, err := openpgp.NewEntity("rsa", "", "rsa@construct", &packet.Config{Algorithm: packet.PubKeyAlgoRSA, RSABits: 2048})
	must(t, err)
	if _, err := ParseKeyring(publicKeyring(t, rsaKey, false)); err == nil {
		t.Error("an RSA release key was taken; a release is signed with Ed25519 alone")
	}
	if _, err := ParseKeyring(publicKeyring(t, testKeys()[0], true)); err != nil {
		t.Errorf("an Ed25519 key was refused: %v", err)
	}
}

// A slot's partition UUIDs halve its root hash, and give it back.
func TestSlotUUIDs(t *testing.T) {
	const rh = "0123456789abcdef0123456789abcdeffedcba9876543210fedcba9876543210"
	root, verity := SlotUUIDs(rh)
	if root != "01234567-89ab-cdef-0123-456789abcdef" || verity != "fedcba98-7654-3210-fedc-ba9876543210" {
		t.Errorf("%s %s", root, verity)
	}
	if got := RootHash(root, verity); got != rh {
		t.Errorf("RootHash = %s", got)
	}
}
