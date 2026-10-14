package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/Construct-Linux/os/internal/sign"
)

// A test build is signed with files alone: a release's key (KMS) is refused for it, and taken for
// a release.
func TestRefuseKMS(t *testing.T) {
	if err := refuseKMS(built{Version: "20261004.1", Test: true}, "kms:alias/construct-release"); err == nil {
		t.Error("a test build with a KMS key: no error")
	}
	if err := refuseKMS(built{Version: "20261004.1", Test: true}, ".cache/test-keys/pcr.pem"); err != nil {
		t.Errorf("a test build with a file: %v", err)
	}
	if err := refuseKMS(built{Version: "20261004.1", Release: true}, "kms:alias/construct-pcr"); err != nil {
		t.Errorf("a release with a KMS key: %v", err)
	}
	if err := refuseKMS(built{Version: "20261004.150701"}, "kms:alias/construct-release"); err == nil {
		t.Error("a build without a release's version, with a KMS key: no error")
	}
}

// inOutput runs the test in a directory of its own, whose _output/usb holds what image usb built,
// b: the commands read and write _output relative to where they run.
func inOutput(t *testing.T, b built) {
	t.Helper()
	t.Chdir(t.TempDir())
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(out, "usb", builtFile), raw)
}

func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// filesUnder are the files under dir, every one: what a refused command must not have added to.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var got []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			got = append(got, p)
		}
		return err
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return got
}

// keyFile writes key as a PKCS #8 PEM file, as task keygen does, and answers its path.
func keyFile(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "key.pem")
	writeFile(t, p, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	return p
}

// refusedFor is whether err is a refusal that says why: a command that failed later, for another
// reason, did not refuse.
func refusedFor(err error, why string) bool {
	return err != nil && strings.Contains(err.Error(), why)
}

func newEd25519(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// sign-pcr and usb-sign refuse a release's key (KMS) for a test build, or for a build without a
// release's version, before they open the key: nothing is asked of KMS and nothing is written.
func TestATestBuildIsNotSignedWithAReleasesKey(t *testing.T) {
	const kms = "kms:alias/construct-db"
	for name, b := range map[string]built{
		"a test build":   {Version: "20261004.1", Release: true, Test: true},
		"a build's time": {Version: "20261004.150701"},
	} {
		for cmd, run := range map[string]func() error{
			"sign-pcr": func() error { return signPCR([]string{"-key", kms}) },
			"usb-sign": func() error { return usbSign([]string{"-key", kms, "-cert", "db.crt"}) },
		} {
			t.Run(name+"/"+cmd, func(t *testing.T) {
				inOutput(t, b)
				if err := run(); !refusedFor(err, "signed with a release's key") {
					t.Errorf("not refused: %v", err)
				}
				if got := filesUnder(t, out); !slices.Equal(got, []string{filepath.Join(out, "usb", builtFile)}) {
					t.Errorf("wrote %q", got)
				}
			})
		}
	}
}

// sign-release holds a release directory to the build it is of: a release's key (KMS) signs its
// SHA256SUMS only as the release image usb last built, and that no test build. A KMS key refused
// writes no signature.
func TestAReleasesKeySignsOnlyTheReleaseLastBuilt(t *testing.T) {
	const kms = "kms:alias/construct-release"
	for name, tc := range map[string]struct {
		b   built
		dir string
	}{
		"a test build":            {built{Version: "20261004.1", Release: true, Test: true}, "20261004.1"},
		"another than last built": {built{Version: "20261004.2", Release: true}, "20261004.1"},
		"a build's time":          {built{Version: "20261004.150701"}, "20261004.150701"},
	} {
		t.Run(name, func(t *testing.T) {
			inOutput(t, tc.b)
			dir := filepath.Join(out, "release", tc.dir)
			writeFile(t, filepath.Join(dir, "SHA256SUMS"), []byte("sums\n"))
			if err := signRelease([]string{"-key", kms, "-pubring", "release.pgp", "-dir", dir}); !refusedFor(err, "signed with a release's key") {
				t.Errorf("not refused: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "SHA256SUMS.gpg")); err == nil {
				t.Error("SHA256SUMS.gpg written")
			}
		})
	}
	// What refuseKMSRelease lets by: the release last built, a channel's index, a file key.
	inOutput(t, built{Version: "20261004.1", Release: true})
	for _, c := range []struct{ dir, ref string }{
		{filepath.Join(out, "release", "20261004.1"), kms},
		{filepath.Join(out, "channel", "testing"), kms},
		{filepath.Join(out, "release", "20261004.9"), "release.pem"},
	} {
		if err := refuseKMSRelease(c.dir, c.ref); err != nil {
			t.Errorf("%s with %s: %v", c.dir, c.ref, err)
		}
	}
}

// sign-pcr takes the PCR key as RSA alone: an Ed25519 key is refused before anything is signed.
func TestSignPCRTakesAnRSAKeyOnly(t *testing.T) {
	inOutput(t, built{Version: "20261004.1", Release: true})
	if err := signPCR([]string{"-key", keyFile(t, newEd25519(t))}); !refusedFor(err, "RSA") {
		t.Errorf("an Ed25519 PCR key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "usb", pcrSignature)); err == nil {
		t.Error("a PCR signature written")
	}
}

// secureboot-keys makes each key's certificate once, of RSA keys, and signs the variables each
// time under them: a second run keeps the certificates byte for byte, and the set it signs is the
// set the certificates' holders enroll. An Ed25519 key is refused before anything is written, and
// so is a certificate that is not its key's: a new certificate would be a set no machine holds.
func TestSecureBootKeysKeepTheCertificatesAndSignTheVariablesAgain(t *testing.T) {
	rsaRef, edRef := keyFile(t, newRSA(t)), keyFile(t, newEd25519(t))
	dir := filepath.Join(t.TempDir(), "keys")
	if err := secureBootKeys([]string{"-dir", dir, "-pk", rsaRef, "-kek", rsaRef, "-db", edRef}); !refusedFor(err, "RSA") {
		t.Errorf("an Ed25519 db key: %v", err)
	}
	if got := filesUnder(t, dir); len(got) != 0 {
		t.Errorf("wrote %q", got)
	}
	pkRef, kekRef, dbRef := keyFile(t, newRSA(t)), keyFile(t, newRSA(t)), keyFile(t, newRSA(t))
	run := func() {
		t.Helper()
		if err := secureBootKeys([]string{"-dir", dir, "-pk", pkRef, "-kek", kekRef, "-db", dbRef}); err != nil {
			t.Fatal(err)
		}
	}
	run()
	first := dirContents(t, dir)
	run()
	second := dirContents(t, dir)
	for _, c := range secureBootCerts {
		if !bytes.Equal(first[c], second[c]) {
			t.Errorf("%s made again", c)
		}
	}
	if err := verifyKeySet(dir, second); err != nil {
		t.Error(err)
	}
	other, err := sign.SecureBootCertificate("Construct db", newRSA(t))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, dbCert), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.Raw}))
	if err := secureBootKeys([]string{"-dir", dir, "-pk", pkRef, "-kek", kekRef, "-db", dbRef}); !refusedFor(err, "not its certificate's") {
		t.Errorf("another key's certificate as db's: %v", err)
	}
	for name, b := range dirContents(t, dir) {
		if name != dbCert && !bytes.Equal(b, second[name]) {
			t.Errorf("%s written for a refused set", name)
		}
	}
}

// dirContents are the files directly under dir, by name.
func dirContents(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	got := map[string][]byte{}
	for _, p := range filesUnder(t, dir) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		got[filepath.Base(p)] = b
	}
	return got
}

// verifyKeySet checks the signed variables in files against the certificates in dir, as the
// firmware checks them when it enrolls them (sign.VerifySecureBootKeySet).
func verifyKeySet(dir string, files map[string][]byte) error {
	var certs [3]*x509.Certificate
	for i, c := range secureBootCerts {
		block, _ := pem.Decode(files[c])
		if block == nil {
			return fmt.Errorf("%s: no PEM certificate", filepath.Join(dir, c))
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("%s: %w", c, err)
		}
		certs[i] = cert
	}
	return sign.VerifySecureBootKeySet(files, certs[0], certs[1], certs[2])
}

// The committed key set, keys/secureboot, is the one machines hold: its certificates (and
// release.pgp) never change from what origin/main has - a certificate made again for the same
// key is a stranger to every machine that enrolled the first, as release 20261014.1 was - and
// its variables hold what internal/sign puts in them, signed under those certificates, so a
// machine that enrolled the set enrolls them again when they change (task keys:kms --force).
func TestTheCommittedKeySetIsTheOneMachinesHold(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "keys", "secureboot")
	if err := verifyKeySet(dir, dirContents(t, dir)); err != nil {
		t.Errorf("%s: %v (task keys:kms --force signs the variables again under the certificates)", dir, err)
	}
	git := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
		return string(out), err
	}
	base, err := git("merge-base", "HEAD", "origin/main")
	if err != nil {
		t.Skipf("no merge-base with origin/main to compare the keys with: %v", err)
	}
	held := []string{"keys/release.pgp"}
	for _, c := range secureBootCerts {
		held = append(held, "keys/secureboot/"+c)
	}
	changed, err := git(append([]string{"diff", "--name-only", strings.TrimSpace(base), "--"}, held...)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range strings.Fields(changed) {
		t.Errorf("%s changed since origin/main: it is what machines hold, and what they hold signs for them alone", p)
	}
}

// release-key makes release.pgp once, of an Ed25519 key: an RSA key is refused, and a release.pgp
// already there - the key machines trust - is never replaced.
func TestTheReleaseKeyIsMadeOnceOfAnEd25519Key(t *testing.T) {
	to := filepath.Join(t.TempDir(), "release.pgp")
	if err := releaseKey([]string{"-key", keyFile(t, newRSA(t)), "-o", to}); !refusedFor(err, "Ed25519") {
		t.Errorf("an RSA release key: %v", err)
	}
	if _, err := os.Stat(to); err == nil {
		t.Error("release.pgp written for an RSA key")
	}
	writeFile(t, to, []byte("the trusted key"))
	if err := releaseKey([]string{"-key", keyFile(t, newEd25519(t)), "-o", to}); !refusedFor(err, "exists") {
		t.Errorf("a second release.pgp: %v", err)
	}
	if b, _ := os.ReadFile(to); string(b) != "the trusted key" {
		t.Errorf("release.pgp is now %q", b)
	}
}

// check-key fails the build for a release keyring with a key that is not Ed25519.
func TestCheckKeyRefusesAKeyringThatIsNotEd25519(t *testing.T) {
	for algo, want := range map[packet.PublicKeyAlgorithm]bool{packet.PubKeyAlgoEdDSA: true, packet.PubKeyAlgoRSA: false} {
		e, err := openpgp.NewEntity("check-key", "", "check-key@construct", &packet.Config{Algorithm: algo, RSABits: 2048})
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if err := e.Serialize(&b); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "release.pgp")
		writeFile(t, p, b.Bytes())
		if err := checkKey([]string{p}); (err == nil) != want {
			t.Errorf("a keyring of %v: %v", algo, err)
		}
	}
}

// sign-release checks its signature as a machine would before writing it, against release.pgp as
// a machine reads it: a release.pgp that also holds a key other than Ed25519 is refused though the
// release key is in it, and no signature is written.
func TestSignReleaseRefusesAPubringMachinesWouldRefuse(t *testing.T) {
	key := newEd25519(t)
	k, err := sign.NewKey(key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pubring, err := k.Export("Construct release")
	if err != nil {
		t.Fatal(err)
	}
	other, err := openpgp.NewEntity("rsa", "", "rsa@construct", &packet.Config{Algorithm: packet.PubKeyAlgoRSA, RSABits: 2048})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := other.Serialize(&b); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pubringFile, sums := filepath.Join(dir, "release.pgp"), filepath.Join(dir, "SHA256SUMS")
	writeFile(t, pubringFile, append(pubring, b.Bytes()...))
	writeFile(t, sums, []byte("sums\n"))
	err = signRelease([]string{"-key", keyFile(t, key), "-pubring", pubringFile, "-dir", dir})
	if !refusedFor(err, "not Ed25519") {
		t.Errorf("a pubring with an RSA key: %v", err)
	}
	if _, err := os.Stat(sums + ".gpg"); err == nil {
		t.Error("SHA256SUMS.gpg written")
	}
}
