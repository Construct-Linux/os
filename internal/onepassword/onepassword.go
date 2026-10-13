// Package onepassword installs 1Password for Linux on the machine, from 1Password's own servers -
// the image does not carry it, as nothing confirms it may be redistributed - checked against
// their code-signing key, which the verified root carries and which is pinned here by its
// fingerprint as well. The native app, not the Flatpak: a Flatpak cannot reach the browser
// extension, the SSH agent or the system unlock. What its own after-install.sh would write in
// /usr - the polkit actions, the groups, the desktop entry - the image carries already.
package onepassword

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
)

const (
	// URL is the stable channel's newest tarball; its detached signature is URL+".sig".
	URL = "https://downloads.1password.com/linux/tar/stable/x86_64/1password-latest.tar.gz"
	// Key is where the verified root carries 1Password's code-signing key.
	Key = "/usr/share/construct/1password.asc"
	// Fingerprint is that key's: a key file that is not it is refused.
	Fingerprint = "3fef9748469adbe15da7ca80ac2d62742012ea22"
	// Dir is where it is installed: /opt, which is /var/opt.
	Dir = "/opt/1Password"
	// etagFile records the ETag of the tarball installed: an update asks for the headers alone
	// and downloads 200 MB only when they changed.
	etagFile = ".construct-etag"
)

// Options say where from, checked by which key, and where to.
type Options struct {
	URL, Key, Dir string
}

// Installed says whether 1Password is installed in dir.
func Installed(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "1password"))
	return err == nil
}

// Install downloads the tarball and its signature, checks them, extracts it beside o.Dir refusing
// anything that would leave the tree, and swaps it in whole: a 1Password running from the old
// tree keeps its open files. It answers the version installed, or "" when o.Dir already holds the
// tarball the server has (its ETag).
func Install(ctx context.Context, o Options) (string, error) {
	keys, err := keyring(o.Key)
	if err != nil {
		return "", err
	}
	etag, err := headETag(ctx, o.URL)
	if err != nil {
		return "", err
	}
	if was, _ := os.ReadFile(filepath.Join(o.Dir, etagFile)); etag != "" && string(was) == etag {
		return "", nil
	}
	tmp, err := os.MkdirTemp("/var/tmp", "construct-1password-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	tarball, sig := filepath.Join(tmp, "1password.tar.gz"), filepath.Join(tmp, "1password.tar.gz.sig")
	if err := download(ctx, o.URL, tarball); err != nil {
		return "", err
	}
	if err := download(ctx, o.URL+".sig", sig); err != nil {
		return "", err
	}
	if err := checkDetached(keys, tarball, sig); err != nil {
		return "", err
	}
	staged := o.Dir + ".new"
	if err := os.RemoveAll(staged); err != nil {
		return "", err
	}
	version, err := extractTop(tarball, staged)
	if err != nil {
		return "", err
	}
	if err := permissions(staged); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(staged, etagFile), []byte(etag), 0o644); err != nil {
		return "", err
	}
	old := o.Dir + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(o.Dir, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(staged, o.Dir); err != nil {
		return "", err
	}
	_ = os.RemoveAll(old)
	return version, nil
}

// keyring is 1Password's code-signing key, refused unless it is the one pinned.
func keyring(path string) (openpgp.EntityList, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	keys, err := openpgp.ReadArmoredKeyRing(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(keys) != 1 || hex.EncodeToString(keys[0].PrimaryKey.Fingerprint) != Fingerprint {
		return nil, fmt.Errorf("%s is not 1Password's code-signing key %s", path, Fingerprint)
	}
	return keys, nil
}

// checkDetached checks file against its detached signature sig, made by a key in keys.
func checkDetached(keys openpgp.KeyRing, file, sig string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	s, err := os.ReadFile(sig)
	if err != nil {
		return err
	}
	check := openpgp.CheckDetachedSignature
	if bytes.HasPrefix(bytes.TrimSpace(s), []byte("-----BEGIN PGP SIGNATURE")) {
		check = openpgp.CheckArmoredDetachedSignature
	}
	if _, err := check(keys, f, bytes.NewReader(s), nil); err != nil {
		return fmt.Errorf("%s is not signed by 1Password: %w", filepath.Base(file), err)
	}
	return nil
}

func headETag(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp.Header.Get("ETag"), nil
}

func download(ctx context.Context, url, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// extractTop extracts the gzipped tarball's one top directory into dest, and answers the version
// its name carries (1password-8.12.38.x64: 8.12.38). Nothing is written outside dest: every
// write goes through an os.Root, which refuses a name or a chain of links that resolves outside
// it, and a link that names a place outside the tree is refused as well.
func extractTop(tarball, dest string) (string, error) {
	f, err := os.Open(tarball)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return "", err
	}
	defer root.Close()
	tr := tar.NewReader(gz)
	top := ""
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		first, rest, _ := strings.Cut(strings.TrimPrefix(filepath.ToSlash(h.Name), "./"), "/")
		if top == "" {
			top = first
		}
		if first != top {
			return "", fmt.Errorf("%s: a second top directory, %s", filepath.Base(tarball), first)
		}
		rest = strings.TrimSuffix(rest, "/")
		if rest == "" {
			continue
		}
		name := filepath.FromSlash(rest)
		if !filepath.IsLocal(name) {
			return "", fmt.Errorf("%s leaves the tree", h.Name)
		}
		if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return "", err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			// Neither setuid nor setgid from the archive: permissions sets the one bit
			// that is wanted.
			out, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o755)
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return "", err
			}
			if err := out.Close(); err != nil {
				return "", err
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) || !filepath.IsLocal(filepath.Join(filepath.Dir(name), h.Linkname)) {
				return "", fmt.Errorf("%s links out of the tree, to %s", h.Name, h.Linkname)
			}
			if err := root.Symlink(h.Linkname, name); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("%s: a %c entry", h.Name, h.Typeflag)
		}
	}
	version := strings.TrimSuffix(strings.TrimPrefix(top, "1password-"), ".x64")
	if top == "" || version == top {
		return "", fmt.Errorf("%s: no 1password-<version> directory", filepath.Base(tarball))
	}
	if _, err := root.Stat("1password"); err != nil {
		return "", fmt.Errorf("%s: no 1password in it: %w", filepath.Base(tarball), err)
	}
	return version, nil
}

// permissions does what 1Password's after-install.sh does to the tree, but for
// chrome-sandbox's setuid bit: the kernel gives unprivileged user namespaces, which Electron's
// sandbox uses instead, as VS Code's does (recipes/code). The browser helper and the MCP server
// are setgid to groups of their own (usr/lib/sysusers.d/construct-1password.conf), which only
// guards them against their environment being tampered with, as 1Password says.
func permissions(dir string) error {
	for file, group := range map[string]string{"1Password-BrowserSupport": "onepassword", "1password-mcp": "onepassword-mcp"} {
		path := filepath.Join(dir, file)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		g, err := user.LookupGroup(group)
		if err != nil {
			return err
		}
		gid, err := strconv.Atoi(g.Gid)
		if err != nil {
			return err
		}
		if err := os.Chown(path, 0, gid); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o755|os.ModeSetgid); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "1password-mcp")); err == nil {
		_ = os.Symlink("1password-mcp", filepath.Join(dir, "onepassword-mcp"))
	}
	return nil
}
