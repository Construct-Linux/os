// Package lenovo fetches, from Lenovo's own servers, the Windows driver packages of a Lenovo
// machine that carry firmware the image cannot: a laptop maker's build its license keeps out of
// linux-firmware (construct firmware, cmd/construct). Lenovo's System Update reads the same
// catalog: one per machine type (the first four characters of the DMI product name, 21VV for the
// ThinkPad X9-15p), naming each package's descriptor and its SHA-256; the descriptor names the
// package's installer, its size and SHA-256, and the PCI ids of the hardware it is for - how a
// package is matched to a device here, as System Update matches it on Windows. The installers
// are Inno Setup's (their ExtractCommand is its /VERYSILENT /EXTRACT), which innoextract lists
// and unpacks, file by file, without running anything.
package lenovo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/program"
)

// CatalogURL is the catalog of a machine type's packages for Windows 11, which every machine
// Construct runs on is new enough to have.
const CatalogURL = "https://download.lenovo.com/catalog/%s_Win11.xml"

// A Package is one of a machine type's, as its catalog and descriptor say.
type Package struct {
	ID, Title, Version string
	// Descriptor is the URL of its descriptor; the installer is beside it.
	Descriptor string
	// HardwareIDs are the PCI devices it is for, vendor:device in lower-case hex (8086:e345).
	HardwareIDs []string
	// Installer is its .exe: name, size and SHA-256 (the descriptor calls it a CRC).
	Installer File
}

// A File of a package.
type File struct {
	Name   string
	Size   int64
	SHA256 string
}

// InstallerURL is where the installer is: beside the descriptor.
func (p Package) InstallerURL() string {
	return p.Descriptor[:strings.LastIndex(p.Descriptor, "/")+1] + p.Installer.Name
}

// For is true when the package is for one of ids, PCI vendor:device pairs.
func (p Package) For(ids []string) bool {
	for _, id := range ids {
		for _, h := range p.HardwareIDs {
			if h == id {
				return true
			}
		}
	}
	return false
}

type catalogEntry struct {
	Location string
	Category string
	Checksum string
}

// ParseCatalog reads a machine type's catalog: each package's descriptor URL and the SHA-256
// of the descriptor.
func ParseCatalog(r io.Reader) ([]catalogEntry, error) {
	var c struct {
		Packages []struct {
			Location string `xml:"location"`
			Category string `xml:"category"`
			Checksum struct {
				Type  string `xml:"type,attr"`
				Value string `xml:",chardata"`
			} `xml:"checksum"`
		} `xml:"package"`
	}
	if err := xml.NewDecoder(r).Decode(&c); err != nil {
		return nil, fmt.Errorf("the catalog: %w", err)
	}
	var entries []catalogEntry
	for _, p := range c.Packages {
		if p.Checksum.Type != "sha256" || len(p.Checksum.Value) != 64 {
			return nil, fmt.Errorf("the catalog: %s carries a %q checksum, not a sha256", p.Location, p.Checksum.Type)
		}
		entries = append(entries, catalogEntry{p.Location, p.Category, strings.ToLower(p.Checksum.Value)})
	}
	return entries, nil
}

var pnpPCI = regexp.MustCompile(`(?i)^PCI\\VEN_([0-9A-F]{4})&DEV_([0-9A-F]{4})`)

// ParsePackage reads a package's descriptor, found at url.
func ParsePackage(b []byte, url string) (Package, error) {
	var d struct {
		ID      string `xml:"id,attr"`
		Version string `xml:"version,attr"`
		Title   struct {
			Desc []string `xml:"Desc"`
		} `xml:"Title"`
		PnPIDs []string `xml:"DetectVersion>_PnPID"`
		Files  struct {
			Installer struct {
				File []struct {
					Name string `xml:"Name"`
					Size string `xml:"Size"`
					CRC  string `xml:"CRC"`
				} `xml:"File"`
			} `xml:"Installer"`
		} `xml:"Files"`
	}
	if err := xml.Unmarshal(b, &d); err != nil {
		return Package{}, fmt.Errorf("%s: %w", path.Base(url), err)
	}
	p := Package{ID: d.ID, Version: d.Version, Descriptor: url}
	if len(d.Title.Desc) > 0 {
		p.Title = strings.TrimSpace(d.Title.Desc[0])
	}
	for _, id := range d.PnPIDs {
		if m := pnpPCI.FindStringSubmatch(strings.TrimSpace(id)); m != nil {
			h := strings.ToLower(m[1] + ":" + m[2])
			if !contains(p.HardwareIDs, h) {
				p.HardwareIDs = append(p.HardwareIDs, h)
			}
		}
	}
	for _, f := range d.Files.Installer.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".exe") {
			continue
		}
		size, err := strconv.ParseInt(strings.TrimSpace(f.Size), 10, 64)
		if err != nil || len(f.CRC) != 64 {
			return Package{}, fmt.Errorf("%s: installer %s without a size and a sha256", path.Base(url), f.Name)
		}
		if p.Installer.Name != "" {
			return Package{}, fmt.Errorf("%s: two installers, %s and %s", path.Base(url), p.Installer.Name, f.Name)
		}
		p.Installer = File{strings.TrimSpace(f.Name), size, strings.ToLower(f.CRC)}
	}
	if p.Installer.Name == "" {
		return Package{}, fmt.Errorf("%s: no installer", path.Base(url))
	}
	return p, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Catalog fetches a machine type's packages: the catalog, then every descriptor it names,
// each checked against the catalog's digest of it.
func Catalog(ctx context.Context, machineType string) ([]Package, error) {
	b, err := get(ctx, fmt.Sprintf(CatalogURL, machineType))
	if err != nil {
		return nil, fmt.Errorf("no catalog for a Lenovo machine of type %s: %w", machineType, err)
	}
	entries, err := ParseCatalog(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	var packages []Package
	for _, e := range entries {
		d, err := get(ctx, e.Location)
		if err != nil {
			return nil, err
		}
		if got := digest(d); got != e.Checksum {
			return nil, fmt.Errorf("%s: sha256 %s, the catalog says %s", path.Base(e.Location), got, e.Checksum)
		}
		p, err := ParsePackage(d, e.Location)
		if err != nil {
			return nil, err
		}
		packages = append(packages, p)
	}
	return packages, nil
}

func get(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Download fetches the package's installer into dir, checked against the size and digest its
// descriptor gives, and answers its path.
func Download(ctx context.Context, p Package, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.InstallerURL(), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", p.InstallerURL(), resp.Status)
	}
	file := filepath.Join(dir, p.Installer.Name)
	f, err := os.Create(file)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, p.Installer.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n != p.Installer.Size {
		return "", fmt.Errorf("%s: %d bytes, its descriptor says %d", p.Installer.Name, n, p.Installer.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != p.Installer.SHA256 {
		return "", fmt.Errorf("%s: sha256 %s, its descriptor says %s", p.Installer.Name, got, p.Installer.SHA256)
	}
	return file, nil
}

var listed = regexp.MustCompile(`^ - "(.*)"`)

// ParseListing reads innoextract's listing (-l -q) of an installer: the files in it, each once.
func ParseListing(r io.Reader) []string {
	var files []string
	seen := map[string]bool{}
	for _, line := range strings.Split(readAll(r), "\n") {
		m := listed.FindStringSubmatch(line)
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		files = append(files, m[1])
	}
	return files
}

func readAll(r io.Reader) string {
	b, _ := io.ReadAll(r)
	return string(b)
}

// List is what the installer holds, by innoextract.
func List(ctx context.Context, installer string) ([]string, error) {
	var out bytes.Buffer
	if err := (program.With{Stdout: &out}).Run(ctx, "innoextract", "--list", "--quiet", installer); err != nil {
		return nil, err
	}
	return ParseListing(&out), nil
}

// Extract unpacks files, paths as List gave them, from the installer into dir, where each
// keeps its path.
func Extract(ctx context.Context, installer, dir string, files []string) error {
	args := []string{"--silent", "--output-dir", dir}
	for _, f := range files {
		args = append(args, "--include", f)
	}
	args = append(args, installer)
	if err := program.Run(ctx, "innoextract", args...); err != nil {
		return err
	}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			return fmt.Errorf("innoextract left %s out of %s", f, filepath.Base(installer))
		}
	}
	return nil
}
