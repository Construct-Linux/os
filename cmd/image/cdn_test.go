package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Construct-Linux/os/internal/sign"
	"github.com/Construct-Linux/os/internal/update"
)

// The test CDN serves each channel's signed index, and a release file as a redirect to the file,
// as the update CDN does to GitHub: stable lists v1 alone.
func TestUpdateCDN(t *testing.T) {
	dir := t.TempDir()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	keyFile := filepath.Join(dir, "release.pem")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	k, _ := sign.NewKey(priv, time.Now())
	pubring, _ := k.Export("test")
	pubFile := filepath.Join(dir, "release.pgp")
	if err := os.WriteFile(pubFile, pubring, 0o644); err != nil {
		t.Fatal(err)
	}
	versions := map[string]string{"v1": "20261004.1", "v2": "20261004.2"}
	for v, ver := range versions {
		rel := filepath.Join(dir, v+".release")
		if err := os.MkdirAll(rel, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rel, "SHA256SUMS"), []byte(releaseSums(ver)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rel, "construct_"+ver+".efi"), []byte("kernel "+ver), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := newUpdateCDN(dir, versions, keyFile, pubFile)
	if err != nil {
		t.Fatal(err)
	}
	defer c.srv.Close()
	base := strings.Replace(c.url, "10.0.2.2", "127.0.0.1", 1)
	get := func(path string) string {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	stable := get("/stable/SHA256SUMS")
	if strings.Contains(stable, "20261004.2") || !strings.Contains(stable, "construct_20261004.1.efi") {
		t.Errorf("stable's index:\n%s", stable)
	}
	sig := get("/testing/SHA256SUMS.gpg")
	keys, err := update.ParseKeyring(pubring)
	if err != nil {
		t.Fatal(err)
	}
	if err := update.Verify(keys, []byte(get("/testing/SHA256SUMS")), []byte(sig)); err != nil {
		t.Errorf("testing's index does not verify: %v", err)
	}
	if got := get("/testing/construct_20261004.2.efi"); got != "kernel 20261004.2" || c.redirects.Load() != 1 {
		t.Errorf("the kernel image through the redirect: %q, %d redirects", got, c.redirects.Load())
	}
	// Only what an index lists is a file: a version no release has, a file in another release's
	// slot, a release directory's other files, and a path out of it are all nothing.
	for _, p := range []string{"/testing/construct_20261004.3.efi", "/assets/v1/construct_20261004.2.efi", "/assets/v2/SHA256SUMS", "/assets/v2/../release.pem"} {
		resp, err := http.Get(base + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %s, not 404", p, resp.Status)
		}
	}
	if c.redirects.Load() != 1 {
		t.Errorf("%d redirects after the refused requests, not 1", c.redirects.Load())
	}
}
