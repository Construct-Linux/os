package sign

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// fakeKMS signs as KMS does for the two kinds of key, with an in-memory key, and keeps what it
// was asked.
type fakeKMS struct {
	key   crypto.Signer
	asked []kms.SignInput
}

func (f *fakeKMS) GetPublicKey(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	der, err := x509.MarshalPKIXPublicKey(f.key.Public())
	if err != nil {
		return nil, err
	}
	return &kms.GetPublicKeyOutput{PublicKey: der, KeyUsage: types.KeyUsageTypeSignVerify}, nil
}

func (f *fakeKMS) Sign(_ context.Context, in *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	f.asked = append(f.asked, *in)
	switch in.SigningAlgorithm {
	case types.SigningAlgorithmSpecEd25519Sha512:
		if in.MessageType != types.MessageTypeRaw {
			return nil, errors.New("ED25519_SHA_512 takes a RAW message")
		}
		sig, err := f.key.Sign(nil, in.Message, crypto.Hash(0))
		return &kms.SignOutput{Signature: sig}, err
	case types.SigningAlgorithmSpecRsassaPkcs1V15Sha256:
		sig, err := f.key.Sign(rand.Reader, in.Message, crypto.SHA256)
		return &kms.SignOutput{Signature: sig}, err
	}
	return nil, errors.New("an algorithm the fake does not sign with")
}

// A release signed through KMS verifies as one signed with the key in memory.
func TestKMSEd25519(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &fakeKMS{key: priv}
	s, err := newKMSSigner(context.Background(), f, "alias/construct-release")
	if err != nil {
		t.Fatal(err)
	}
	k, err := NewKey(s, time.Unix(1791100000, 0))
	if err != nil {
		t.Fatal(err)
	}
	pubring, err := k.Export("Construct release")
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("SHA256SUMS")
	sig, err := k.DetachSign(bytes.NewReader(data), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(pubring, bytes.NewReader(data), sig); err != nil {
		t.Fatal(err)
	}
	if n := len(f.asked); n != 2 {
		t.Errorf("KMS asked %d times, want 2 (the self-signature, the release)", n)
	}
}

// An RSA key signs a SHA-256 digest in PKCS #1 v1.5 and nothing else: a PSS or SHA-1 request is
// refused before it reaches KMS.
func TestKMSRSA(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeKMS{key: priv}
	s, err := newKMSSigner(context.Background(), f, "alias/construct-pcr")
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("policy"))
	sig, err := s.Sign(rand.Reader, h[:], crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(&priv.PublicKey, crypto.SHA256, h[:], sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sign(rand.Reader, h[:], &rsa.PSSOptions{Hash: crypto.SHA256}); err == nil {
		t.Error("PSS: no error")
	}
	if _, err := s.Sign(rand.Reader, h[:20], crypto.SHA1); err == nil {
		t.Error("SHA-1: no error")
	}
	if len(f.asked) != 1 {
		t.Errorf("KMS asked %d times, want 1", len(f.asked))
	}
}

// A key of a kind the repository does not sign with is refused when it is opened.
func TestKMSRefusesOtherKeys(t *testing.T) {
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newKMSSigner(context.Background(), &fakeKMS{key: small}, "k"); err == nil {
		t.Error("RSA-1024: no error")
	}
}
