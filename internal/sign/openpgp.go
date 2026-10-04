package sign

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// The OpenPGP a release needs, written here because a KMS key is a crypto.Signer and OpenPGP
// libraries sign EdDSA only with a key in memory: a version 4 Ed25519 public key (the legacy EdDSA
// algorithm, 22, which gpg makes and every verifier takes), its self-signature, and a detached
// signature over a file - systemd-sysupdate's SHA256SUMS.gpg. RFC 4880 and RFC 9580's v4 forms;
// SHA-512 is the hash, as gpg uses for Ed25519.

const (
	tagSignature = 2
	tagPublicKey = 6
	tagUserID    = 13

	algoEdDSA  = 22
	hashSHA512 = 10

	sigBinary   = 0x00
	sigPositive = 0x13

	subCreated  = 2
	subIssuer   = 16
	subHashes   = 21
	subKeyFlags = 27
	subIssuerFP = 33
)

// ed25519OID is Ed25519's curve in the legacy EdDSA algorithm, 1.3.6.1.4.1.11591.15.1.
var ed25519OID = []byte{0x2B, 0x06, 0x01, 0x04, 0x01, 0xDA, 0x47, 0x0F, 0x01}

// Key is an Ed25519 signer as an OpenPGP key: its creation time is part of its fingerprint, so a
// key is the signer and the time it was first exported with (ReadKey finds it again).
type Key struct {
	signer  crypto.Signer
	created time.Time
	body    []byte
	fpr     [20]byte
}

// NewKey is s as an OpenPGP key created at created.
func NewKey(s crypto.Signer, created time.Time) (*Key, error) {
	pub, ok := s.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("sign: an OpenPGP release key is Ed25519")
	}
	var b bytes.Buffer
	b.WriteByte(4)
	_ = binary.Write(&b, binary.BigEndian, uint32(created.Unix()))
	b.WriteByte(algoEdDSA)
	b.WriteByte(byte(len(ed25519OID)))
	b.Write(ed25519OID)
	// The point, prefixed 0x40 (native form), as a multiprecision integer.
	b.Write(mpi(append([]byte{0x40}, pub...)))
	k := &Key{signer: s, created: created.Truncate(time.Second), body: b.Bytes()}
	h := sha1.New()
	h.Write(hashedKey(k.body))
	copy(k.fpr[:], h.Sum(nil))
	return k, nil
}

// ReadKey is s as the key in pubring, which must hold it: the fingerprint signatures carry is the
// one machines trust, so the key is never made anew at signing.
func ReadKey(s crypto.Signer, pubring []byte) (*Key, error) {
	pub, ok := s.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("sign: an OpenPGP release key is Ed25519")
	}
	keys, err := openpgp.ReadKeyRing(bytes.NewReader(pubring))
	if err != nil {
		return nil, fmt.Errorf("sign: the release keyring: %w", err)
	}
	for _, e := range keys {
		k, err := NewKey(s, e.PrimaryKey.CreationTime)
		if err != nil {
			return nil, err
		}
		if k.fpr == [20]byte(e.PrimaryKey.Fingerprint) {
			return k, nil
		}
	}
	return nil, fmt.Errorf("sign: the release keyring has no key %x", []byte(pub))
}

// Fingerprint is the key's v4 fingerprint.
func (k *Key) Fingerprint() [20]byte { return k.fpr }

// Export is the transferable public key: the key, the user ID, and its positive self-signature,
// flagged to certify and sign, which is what import-pubring.pgp holds.
func (k *Key) Export(uid string) ([]byte, error) {
	sub := append(subpacket(subCreated, be32(uint32(k.created.Unix()))),
		subpacket(subKeyFlags, []byte{0x03})...)
	sub = append(sub, subpacket(subHashes, []byte{hashSHA512, 8})...)
	sub = append(sub, subpacket(subIssuerFP, append([]byte{4}, k.fpr[:]...))...)
	signed := hashedKey(k.body)
	signed = append(signed, 0xB4)
	signed = append(signed, be32(uint32(len(uid)))...)
	signed = append(signed, uid...)
	sig, err := k.signature(sigPositive, sub, bytes.NewReader(signed))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(packetOf(tagPublicKey, k.body))
	out.Write(packetOf(tagUserID, []byte(uid)))
	out.Write(sig)
	return out.Bytes(), nil
}

// DetachSign is a binary document signature over data made at, as gpg --detach-sign writes it.
func (k *Key) DetachSign(data io.Reader, at time.Time) ([]byte, error) {
	sub := append(subpacket(subCreated, be32(uint32(at.Unix()))),
		subpacket(subIssuerFP, append([]byte{4}, k.fpr[:]...))...)
	return k.signature(sigBinary, sub, data)
}

// signature is a v4 signature packet of kind over data, with the hashed subpackets sub and the
// issuer's key ID unhashed (for verifiers that look for it there).
func (k *Key) signature(kind byte, sub []byte, data io.Reader) ([]byte, error) {
	hashed := []byte{4, kind, algoEdDSA, hashSHA512}
	hashed = binary.BigEndian.AppendUint16(hashed, uint16(len(sub)))
	hashed = append(hashed, sub...)
	h := sha512.New()
	if _, err := io.Copy(h, data); err != nil {
		return nil, err
	}
	h.Write(hashed)
	h.Write([]byte{4, 0xFF})
	h.Write(be32(uint32(len(hashed))))
	digest := h.Sum(nil)
	// Legacy EdDSA signs the digest as its message.
	raw, err := k.signer.Sign(nil, digest, crypto.Hash(0))
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.SignatureSize {
		return nil, fmt.Errorf("sign: an Ed25519 signature of %d bytes", len(raw))
	}
	unhashed := subpacket(subIssuer, k.fpr[12:])
	var b bytes.Buffer
	b.Write(hashed)
	_ = binary.Write(&b, binary.BigEndian, uint16(len(unhashed)))
	b.Write(unhashed)
	b.Write(digest[:2])
	b.Write(mpi(raw[:32]))
	b.Write(mpi(raw[32:]))
	return packetOf(tagSignature, b.Bytes()), nil
}

// Verify checks sig over data against pubring with an OpenPGP implementation other than this
// one: what was signed is checked before it is published.
func Verify(pubring []byte, data io.Reader, sig []byte) error {
	keys, err := openpgp.ReadKeyRing(bytes.NewReader(pubring))
	if err != nil {
		return err
	}
	_, err = openpgp.CheckDetachedSignature(keys, data, bytes.NewReader(sig), &packet.Config{})
	return err
}

// hashedKey is a public key body as fingerprints and certifications hash it.
func hashedKey(body []byte) []byte {
	out := []byte{0x99}
	out = binary.BigEndian.AppendUint16(out, uint16(len(body)))
	return append(out, body...)
}

// packetOf is a packet in the new format.
func packetOf(tag byte, body []byte) []byte {
	out := []byte{0xC0 | tag}
	switch n := len(body); {
	case n < 192:
		out = append(out, byte(n))
	case n < 8384:
		n -= 192
		out = append(out, byte(n>>8)+192, byte(n))
	default:
		out = append(out, 0xFF)
		out = append(out, be32(uint32(n))...)
	}
	return append(out, body...)
}

// subpacket is a signature subpacket; every one here is shorter than 192 bytes.
func subpacket(typ byte, data []byte) []byte {
	return append([]byte{byte(len(data) + 1), typ}, data...)
}

// mpi is a multiprecision integer: its length in bits, then its bytes without leading zeros.
func mpi(b []byte) []byte {
	b = bytes.TrimLeft(b, "\x00")
	n := 0
	if len(b) > 0 {
		n = (len(b)-1)*8 + bits.Len8(b[0])
	}
	return append(binary.BigEndian.AppendUint16(nil, uint16(n)), b...)
}

func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
