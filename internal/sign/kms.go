package sign

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// kmsAPI is the part of KMS a signer uses: a fake stands in for it in tests.
type kmsAPI interface {
	GetPublicKey(ctx context.Context, in *kms.GetPublicKeyInput, opts ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
	Sign(ctx context.Context, in *kms.SignInput, opts ...func(*kms.Options)) (*kms.SignOutput, error)
}

// kmsSigner signs with a KMS key: the key never leaves KMS, and each signature is in CloudTrail.
type kmsSigner struct {
	ctx context.Context
	api kmsAPI
	id  string
	pub crypto.PublicKey
}

// openKMS is the signer for the KMS key id (a key ID, ARN or alias/...), with the credentials and
// region the environment gives (on the builder, its instance role and AWS_REGION).
func openKMS(ctx context.Context, id string) (crypto.Signer, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("sign: AWS configuration: %w", err)
	}
	return newKMSSigner(ctx, kms.NewFromConfig(cfg), id)
}

func newKMSSigner(ctx context.Context, api kmsAPI, id string) (*kmsSigner, error) {
	out, err := api.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: aws.String(id)})
	if err != nil {
		return nil, fmt.Errorf("sign: the public key of %s: %w", id, err)
	}
	if out.KeyUsage != types.KeyUsageTypeSignVerify {
		return nil, fmt.Errorf("sign: %s is for %s, not signing", id, out.KeyUsage)
	}
	pub, err := x509.ParsePKIXPublicKey(out.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("sign: the public key of %s: %w", id, err)
	}
	if err := allowed(pub); err != nil {
		return nil, fmt.Errorf("%w (%s)", err, id)
	}
	return &kmsSigner{ctx: ctx, api: api, id: id, pub: pub}, nil
}

func (s *kmsSigner) Public() crypto.PublicKey { return s.pub }

// Sign signs as the crypto.Signer of the key's kind would: an Ed25519 key the message itself
// (opts' hash is zero, KMS's ED25519_SHA_512 over a RAW message: plain Ed25519), an RSA key a
// SHA-256 digest in PKCS #1 v1.5 (RSASSA_PKCS1_V1_5_SHA_256 over a DIGEST). Anything else is
// refused rather than asked of KMS in a form the verifier would not take.
func (s *kmsSigner) Sign(_ io.Reader, msg []byte, opts crypto.SignerOpts) ([]byte, error) {
	in := &kms.SignInput{KeyId: aws.String(s.id), Message: msg}
	switch s.pub.(type) {
	case ed25519.PublicKey:
		if opts.HashFunc() != crypto.Hash(0) {
			return nil, fmt.Errorf("sign: Ed25519 signs a message, not a %v digest", opts.HashFunc())
		}
		in.SigningAlgorithm, in.MessageType = types.SigningAlgorithmSpecEd25519Sha512, types.MessageTypeRaw
	case *rsa.PublicKey:
		if _, pss := opts.(*rsa.PSSOptions); pss || opts.HashFunc() != crypto.SHA256 || len(msg) != crypto.SHA256.Size() {
			return nil, fmt.Errorf("sign: RSA signs a SHA-256 digest in PKCS #1 v1.5, not %v", opts.HashFunc())
		}
		in.SigningAlgorithm, in.MessageType = types.SigningAlgorithmSpecRsassaPkcs1V15Sha256, types.MessageTypeDigest
	}
	out, err := s.api.Sign(s.ctx, in)
	if err != nil {
		return nil, fmt.Errorf("sign: %s: %w", s.id, err)
	}
	return out.Signature, nil
}
