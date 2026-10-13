package sign

import (
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// pemFile writes a PEM block of kind over der in a directory of the test's, and answers its path.
func pemFile(t *testing.T, kind string, der []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// pkcs8 is key as a PKCS #8 private key, as openssl genpkey writes one.
func pkcs8(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// ed25519File is a throwaway Ed25519 key in a PEM file, and the key.
func ed25519File(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pemFile(t, "PRIVATE KEY", pkcs8(t, priv)), priv
}

// A file holds a key the repository signs with, or it is refused when opened: no PEM, a PEM of
// something else, a key that cannot sign, an ECDSA key - NIST curves are no key here - in either
// of openssl's forms.
func TestAFileKeyOfAnotherKindIsRefused(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sec1, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	x, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	noPEM := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(noPEM, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, ref := range map[string]string{
		"no PEM":            noPEM,
		"a certificate":     pemFile(t, "CERTIFICATE", []byte("der")),
		"PKCS #8 garbage":   pemFile(t, "PRIVATE KEY", []byte("der")),
		"ECDSA, PKCS #8":    pemFile(t, "PRIVATE KEY", pkcs8(t, ec)),
		"ECDSA, SEC 1":      pemFile(t, "EC PRIVATE KEY", sec1),
		"X25519, no signer": pemFile(t, "PRIVATE KEY", pkcs8(t, x)),
	} {
		if s, err := Open(context.Background(), ref); err == nil {
			t.Errorf("%s opened as a %T", name, s.Public())
		}
	}
	ed, _ := ed25519File(t)
	if _, err := Open(context.Background(), ed); err != nil {
		t.Errorf("an Ed25519 key was refused: %v", err)
	}
}

// The PCR key and Secure Boot's are RSA alone: an Ed25519 key, which a release is signed with, is
// refused for them (image sign-pcr, secureboot-keys, usb-sign).
func TestAnEd25519KeyIsNoRSAKey(t *testing.T) {
	ref, _ := ed25519File(t)
	if _, err := OpenRSA(context.Background(), ref); err == nil {
		t.Error("OpenRSA took an Ed25519 key")
	}
	_, _, cert := testKeySet(t)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if _, _, err := DBKey(context.Background(), ref, certPEM); err == nil {
		t.Error("DBKey took an Ed25519 key")
	}
}

// db's certificate is a certificate, or db's key is refused with it: no PEM, or a PEM that is none.
func TestDBKeyRefusesWhatIsNoCertificate(t *testing.T) {
	_, key, _ := testKeySet(t)
	ref := pemFile(t, "PRIVATE KEY", pkcs8(t, key))
	for name, certPEM := range map[string][]byte{
		"no PEM":         []byte("not a certificate"),
		"not a cert DER": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("der")}),
	} {
		if _, _, err := DBKey(context.Background(), ref, certPEM); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// What is not an EFI binary is not signed for Secure Boot.
func TestWhatIsNotPEIsNotSigned(t *testing.T) {
	_, key, cert := testKeySet(t)
	if _, err := EFI([]byte("MZ, and nothing a PE image has"), key, cert); err == nil {
		t.Error("signed what is not a PE image")
	}
}

// A PCR policy is signed only as systemd-measure writes one, with the RSA key the kernel image
// carries; anything else is refused and nothing signed.
func TestAPCRPolicyThatIsNotOneIsNotSigned(t *testing.T) {
	key, pub := pcrKey(t)
	edPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edDER, err := x509.MarshalPKIXPublicKey(edPub)
	if err != nil {
		t.Fatal(err)
	}
	phases := func(e ...map[string]any) []byte {
		b, err := json.Marshal(map[string]any{"sha256": e})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for name, tc := range map[string]struct{ digests, pub []byte }{
		"not JSON":              {[]byte("{"), pub},
		"no bank":               {[]byte("{}"), pub},
		"a bank of no phase":    {phases(), pub},
		"a policy that is none": {phases(map[string]any{"pcrs": []int{11}, "pol": "not hex"}), pub},
		"an empty policy":       {phases(map[string]any{"pcrs": []int{11}, "pol": ""}), pub},
		"no carried key":        {digests(1), []byte("no PEM")},
		"a carried key of junk": {digests(1), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("der")})},
		"a carried Ed25519 key": {digests(1), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: edDER})},
	} {
		if out, err := PCRPolicy(tc.digests, key, tc.pub); err == nil || out != nil {
			t.Errorf("%s: signed %s, %v", name, out, err)
		}
	}
}

// A release's OpenPGP key is Ed25519: an RSA key is refused as one, made anew or found in a
// keyring, and a keyring that is none is refused.
func TestAnOpenPGPReleaseKeyIsEd25519(t *testing.T) {
	rsaKey, _ := pcrKey(t)
	if _, err := NewKey(rsaKey, time.Now()); err == nil {
		t.Error("NewKey took an RSA key")
	}
	_, pubring, priv := testKey(t)
	if _, err := ReadKey(rsaKey, pubring); err == nil {
		t.Error("ReadKey took an RSA key")
	}
	if _, err := ReadKey(priv, []byte("not a keyring")); err == nil {
		t.Error("ReadKey took a keyring that is none")
	}
}

// otherUsageKMS is a fake KMS whose key is for another use than signing.
type otherUsageKMS struct{ fakeKMS }

func (f *otherUsageKMS) GetPublicKey(ctx context.Context, in *kms.GetPublicKeyInput, opts ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	out, err := f.fakeKMS.GetPublicKey(ctx, in, opts...)
	if err == nil {
		out.KeyUsage = types.KeyUsageTypeEncryptDecrypt
	}
	return out, err
}

// KMS keys are held to what file keys are: an ECDSA key or one not for signing is refused when
// opened, and an Ed25519 key asked to sign a digest - which KMS would sign as a message, a
// signature nothing verifies - is refused before KMS is asked.
func TestKMSRefusesWhatItDoesNotSignWith(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newKMSSigner(context.Background(), &fakeKMS{key: ec}, "k"); err == nil {
		t.Error("an ECDSA KMS key was opened")
	}
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newKMSSigner(context.Background(), &otherUsageKMS{fakeKMS{key: ed}}, "k"); err == nil {
		t.Error("a KMS key for encryption was opened")
	}
	f := &fakeKMS{key: ed}
	s, err := newKMSSigner(context.Background(), f, "k")
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("SHA256SUMS"))
	if _, err := s.Sign(rand.Reader, h[:], crypto.SHA256); err == nil {
		t.Error("an Ed25519 KMS key signed a digest")
	}
	if len(f.asked) != 0 {
		t.Errorf("KMS asked %d times, want 0", len(f.asked))
	}
}

// shortSigner is an Ed25519 key whose signer answers a signature a byte short, as a KMS that
// misbehaved might.
type shortSigner struct{ ed25519.PrivateKey }

func (s shortSigner) Sign(r io.Reader, msg []byte, opts crypto.SignerOpts) ([]byte, error) {
	sig, err := s.PrivateKey.Sign(r, msg, opts)
	return sig[:len(sig)-1], err
}

// A signature that is not an Ed25519 signature's length is refused, not written into a packet
// that no verifier takes.
func TestAnEd25519SignatureOfAnotherLengthIsRefused(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := NewKey(shortSigner{priv}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Export("short"); err == nil {
		t.Error("a short signature was taken")
	}
}
