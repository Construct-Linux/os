package sign

import (
	"bytes"
	"io"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// Verify checks sig over data against pubring with go-crypto's OpenPGP, an implementation other
// than this package's: what the tests sign verifies elsewhere. image sign-release checks a release
// as a machine does, with internal/update.
func Verify(pubring []byte, data io.Reader, sig []byte) error {
	keys, err := openpgp.ReadKeyRing(bytes.NewReader(pubring))
	if err != nil {
		return err
	}
	_, err = openpgp.CheckDetachedSignature(keys, data, bytes.NewReader(sig), &packet.Config{})
	return err
}
