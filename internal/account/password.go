package account

import (
	"crypto/rand"
	"fmt"

	"github.com/openwall/yescrypt-go"
)

// hashSetting is libxcrypt's default yescrypt cost, as crypt_gensalt(3) writes it for passwd and
// chpasswd: N=4096, r=32, p=1.
const hashSetting = "$y$j9T$"

// HashPassword is pw in crypt(3)'s yescrypt form, a User's HashedPassword: what libxcrypt - which
// pam_unix checks it with - makes for a new password, memory-hard where SHA-512 crypt is a GPU's
// to try fast. Openwall's own Go implementation, with a salt of 16 random bytes as libxcrypt takes.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h, err := yescrypt.Hash([]byte(pw), []byte(hashSetting+crypt64(salt)))
	if err != nil {
		return "", fmt.Errorf("hashing the password: %w", err)
	}
	return string(h), nil
}

// crypt64 is b in crypt(3)'s base 64: its alphabet, least significant bits first, as yescrypt
// reads a salt.
func crypt64(b []byte) string {
	const alphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	var out []byte
	for i := 0; i < len(b); {
		value, bits := uint32(0), 0
		for ; bits < 24 && i < len(b); bits += 8 {
			value |= uint32(b[i]) << bits
			i++
		}
		for ; bits > 0; bits -= 6 {
			out = append(out, alphabet[value&0x3f])
			value >>= 6
		}
	}
	return string(out)
}
