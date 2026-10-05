package sign

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"testing"
)

// What ukify --pcr-private-key wrote for enter-initrd (systemd 261's systemd-measure, a throwaway
// RSA-2048 key): a PKCS#1 v1.5 signature of the policy's SHA-256, as PCRPolicy makes it.
const (
	systemdPublic = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA1A850oh00+m4VNbsBDdT
BDFZZLBvDHxESTFWc5pnSlNLkUZUSWwFczpFLW3+0qG++0BsRj180h/i6BAdh3rr
kB6GggtXjNp+oMUq6XuW8W0ltOdZPV02g3peMqf6AArl3WZYCBzX+dhBP2Bc2UyX
Qf+AnGZOlGoO52hXwucc9x0JP4eK1uORwOpMvfHmWLXSHRl6voXmNMwEShTXDo80
tf5s2QlxvN/EJ0HyIi3ptfdWntyd6TOzCE8KDQ+VsUl/ELXxQgYqbk31fZc5a2wq
LAIOyuoTpKevcknT/YGIWJWbxRryJ95pmVJANaovkRYQeEzpSGQIwyWhzpLD2pix
JwIDAQAB
-----END PUBLIC KEY-----
`
	systemdPolicy    = "1f57dc340c0b47209355ffd30eece9b2f478732c3905614cd06477bd6c4e0a48"
	systemdSignature = "LmsFsJX0tGHaoUMTZNZ56mWQI+rfCJSxTFukcMWFaw7N5cf+FcTRvjbWYbh7q1A0svMd0sda4A/BSqw1vYuix/mv0/HahiyBDuaW92SM+3mlBZqP2wYcr+uX9CY8da0FUH6+UFvT4PfwTgEQJYKNuLlGKy7PiBlM4Z2NAjjRczBBXctg0kECAJBITBL8trCHqS6aeZPUjZcSkxvH8aDJCFUc91LCVAnpBYnbU+yKbetjGyVjn1Bu2I82HSzHDL5m+jsxWFh44mQfq/2V9Ygtp3bl7pK9g+GvxSaL07aDrMoHQ6apcDhr4pLYvTd3LNg7DRNTK90HyLLhh346iHIP5A=="
)

func TestSystemdSignsThePolicysDigest(t *testing.T) {
	block, _ := pem.Decode([]byte(systemdPublic))
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pol, _ := hex.DecodeString(systemdPolicy)
	sig, _ := base64.StdEncoding.DecodeString(systemdSignature)
	h := sha256.Sum256(pol)
	if err := rsa.VerifyPKCS1v15(k.(*rsa.PublicKey), crypto.SHA256, h[:], sig); err != nil {
		t.Fatalf("systemd's signature is not PKCS#1 v1.5 of the policy's SHA-256: %v", err)
	}
}

func pcrKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func digests(phases int) []byte {
	var e []map[string]any
	for i := range phases {
		e = append(e, map[string]any{"pcrs": []int{11}, "pkfp": "ab", "pol": fmt.Sprintf("%064x", i+1)})
	}
	b, _ := json.Marshal(map[string]any{"sha256": e})
	return b
}

// Every phase's policy is signed, each with its own signature.
func TestEveryPhaseIsSigned(t *testing.T) {
	key, pub := pcrKey(t)
	b, err := PCRPolicy(digests(4), key, pub)
	if err != nil {
		t.Fatal(err)
	}
	var p pcrPolicy
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if len(p["sha256"]) != 4 {
		t.Fatalf("signed %d phases", len(p["sha256"]))
	}
	for i, e := range p["sha256"] {
		if e.Policy != fmt.Sprintf("%064x", i+1) {
			t.Errorf("phase %d's policy is %s", i, e.Policy)
		}
		pol, _ := hex.DecodeString(e.Policy)
		sig, _ := base64.StdEncoding.DecodeString(e.Signature)
		h := sha256.Sum256(pol)
		if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, h[:], sig); err != nil {
			t.Errorf("phase %d's signature does not verify: %v", i, err)
		}
	}
}

func TestAKeyTheKernelImageDoesNotCarryIsRefused(t *testing.T) {
	key, _ := pcrKey(t)
	_, other := pcrKey(t)
	if _, err := PCRPolicy(digests(4), key, other); err == nil {
		t.Error("signed with a key the kernel image does not carry")
	}
}
