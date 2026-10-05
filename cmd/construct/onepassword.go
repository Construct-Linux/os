package main

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

// 1Password for Linux, downloaded on the machine from 1Password's own servers - the image does not
// carry it, as nothing confirms it may be redistributed - and checked against their code-signing
// key, which the verified root carries (usr/share/construct/1password.asc) and which is pinned here
// by its fingerprint as well. The native app, not the Flatpak: a Flatpak cannot reach the browser
// extension, the SSH agent or the system unlock. What its own after-install.sh would write in /usr -
// the polkit actions, the groups, the desktop entry - the image carries already.
const (
	onePasswordURL         = "https://downloads.1password.com/linux/tar/stable/x86_64/1password-latest.tar.gz"
	onePasswordKey         = "/usr/share/construct/1password.asc"
	onePasswordFingerprint = "3fef9748469adbe15da7ca80ac2d62742012ea22"
	onePasswordDir         = "/opt/1Password"
	// onePasswordETag records the ETag of the tarball installed: the weekly check asks for the
	// headers alone and downloads 200 MB only when they changed.
	onePasswordETag = ".construct-etag"
)

// onePasswordCmd is construct 1password install | update.
func onePasswordCmd(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) != 1 || (args[0] != "install" && args[0] != "update") {
		return errors.New("1password: install, or update (what the weekly timer runs)")
	}
	if args[0] == "update" {
		if _, err := os.Stat(filepath.Join(onePasswordDir, "1password")); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	keys, err := onePasswordKeyring(onePasswordKey)
	if err != nil {
		return err
	}
	etag, err := headETag(ctx, onePasswordURL)
	if err != nil {
		return err
	}
	if was, _ := os.ReadFile(filepath.Join(onePasswordDir, onePasswordETag)); etag != "" && string(was) == etag {
		fmt.Fprintln(stdout, "1Password is up to date")
		return nil
	}
	tmp, err := os.MkdirTemp("/var/tmp", "construct-1password-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	tarball, sig := filepath.Join(tmp, "1password.tar.gz"), filepath.Join(tmp, "1password.tar.gz.sig")
	fmt.Fprintln(stdout, "downloading 1Password")
	if err := download(ctx, onePasswordURL, tarball); err != nil {
		return err
	}
	if err := download(ctx, onePasswordURL+".sig", sig); err != nil {
		return err
	}
	if err := checkDetached(keys, tarball, sig); err != nil {
		return err
	}
	staged := onePasswordDir + ".new"
	if err := os.RemoveAll(staged); err != nil {
		return err
	}
	version, err := extractTop(tarball, staged)
	if err != nil {
		return err
	}
	if err := onePasswordPermissions(staged); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staged, onePasswordETag), []byte(etag), 0o644); err != nil {
		return err
	}
	// The new tree replaces the old whole: a 1Password running from the old keeps its open files.
	old := onePasswordDir + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(onePasswordDir, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(staged, onePasswordDir); err != nil {
		return err
	}
	_ = os.RemoveAll(old)
	fmt.Fprintf(stdout, "installed 1Password %s\n", version)
	return nil
}

// onePasswordKeyring is 1Password's code-signing key, refused unless it is the one pinned.
func onePasswordKeyring(path string) (openpgp.EntityList, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	keys, err := openpgp.ReadArmoredKeyRing(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(keys) != 1 || hex.EncodeToString(keys[0].PrimaryKey.Fingerprint) != onePasswordFingerprint {
		return nil, fmt.Errorf("%s is not 1Password's code-signing key %s", path, onePasswordFingerprint)
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
// its name carries (1password-8.12.38.x64: 8.12.38). Nothing is written outside dest: a name or a
// link that would leave it is refused.
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
		if rest == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rest))
		if !inside(dest, target) {
			return "", fmt.Errorf("%s leaves the tree", h.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			// Neither setuid nor setgid from the archive: onePasswordPermissions sets the one bit
			// that is wanted.
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o755)
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
			if filepath.IsAbs(h.Linkname) || !inside(dest, filepath.Join(filepath.Dir(target), h.Linkname)) {
				return "", fmt.Errorf("%s links out of the tree, to %s", h.Name, h.Linkname)
			}
			if err := os.Symlink(h.Linkname, target); err != nil {
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
	if _, err := os.Stat(filepath.Join(dest, "1password")); err != nil {
		return "", fmt.Errorf("%s: no 1password in it: %w", filepath.Base(tarball), err)
	}
	return version, nil
}

func inside(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// onePasswordPermissions does what 1Password's after-install.sh does to the tree, but for
// chrome-sandbox's setuid bit: the kernel gives unprivileged user namespaces, which Electron's
// sandbox uses instead, as VS Code's does (recipes/code). The browser helper and the MCP server
// are setgid to groups of their own (usr/lib/sysusers.d/construct-1password.conf), which only
// guards them against their environment being tampered with, as 1Password says.
func onePasswordPermissions(dir string) error {
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
