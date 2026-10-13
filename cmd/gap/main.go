// Command gap measures how far Wolfi is from CONSTRUCT's desktop: it takes the runtime dependency
// closure of GNOME's desktop from Alpine edge (whose packaging is the closest to Wolfi's), and
// reports, per source package, what Wolfi publishes today, what it publishes but stopped
// building, and what it lacks. A missing source package is a melange recipe we would own.
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	wolfiIndex = "https://packages.wolfi.dev/os/x86_64/APKINDEX.tar.gz"
	alpineMain = "https://dl-cdn.alpinelinux.org/alpine/edge/main/x86_64/APKINDEX.tar.gz"
	alpineComm = "https://dl-cdn.alpinelinux.org/alpine/edge/community/x86_64/APKINDEX.tar.gz"
)

// plumbing is what a desktop on this machine needs below the session: graphics, input,
// audio, network, bluetooth, apps from Flathub, firmware updates, power.
var plumbing = []string{
	"mesa-dri-gallium", "mesa-vulkan-intel", "intel-media-driver", "libinput", "xwayland",
	"pipewire", "pipewire-pulse", "wireplumber",
	"networkmanager", "networkmanager-wifi", "bluez",
	"flatpak", "fwupd", "upower", "power-profiles-daemon",
	"font-noto", "font-noto-emoji",
}

// desktop is what CONSTRUCT's desktop asks of a distribution: GNOME's session and core, over the
// plumbing.
var desktop = append([]string{
	"gdm", "gnome-shell", "gnome-session", "gnome-settings-daemon", "gnome-control-center",
	"nautilus", "ptyxis", "xdg-desktop-portal-gnome", "gnome-keyring", "gvfs",
}, plumbing...)

// replaced are Alpine packages Wolfi answers with another: Alpine runs OpenRC and elogind
// on musl, Wolfi systemd on glibc.
var replaced = map[string]string{
	"elogind": "systemd", "elogind-libs": "systemd", "polkit-elogind": "polkit",
	"polkit-elogind-libs": "polkit", "libelogind": "systemd", "eudev": "udev", "eudev-libs": "udev", "dbus-x11": "dbus",
}

func skipped(name string) bool {
	for _, p := range []string{"openrc", "musl", "busybox", "alpine-", "apk-tools", "ifupdown", "libc-utils", "scanelf", "ssl_client"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return strings.HasSuffix(name, "-openrc")
}

type pkg struct {
	name, version, origin string
	built                 time.Time
	deps, provides        []string
}

type index struct {
	pkgs      map[string]pkg    // newest build of each name
	providers map[string]string // so:/cmd:/pc: and plain names to a package name
}

func load(urls ...string) (*index, error) {
	ix := &index{pkgs: map[string]pkg{}, providers: map[string]string{}}
	for _, u := range urls {
		if err := ix.read(u); err != nil {
			return nil, fmt.Errorf("%s: %w", u, err)
		}
	}
	return ix, nil
}

// client gives up on an index that stalls: a CDN that stops answering would hang task gap.
var client = &http.Client{Timeout: 2 * time.Minute}

func (ix *index) read(url string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return err
		}
		if h.Name == "APKINDEX" {
			return ix.parse(tr)
		}
	}
}

func (ix *index) parse(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	var p pkg
	var provides []string
	flush := func() {
		if p.name == "" {
			return
		}
		p.provides = provides
		if old, ok := ix.pkgs[p.name]; !ok || p.built.After(old.built) {
			ix.pkgs[p.name] = p
		}
		ix.providers[p.name] = p.name
		for _, pr := range provides {
			k, _, _ := strings.Cut(pr, "=")
			if _, ok := ix.providers[k]; !ok {
				ix.providers[k] = p.name
			}
		}
		p, provides = pkg{}, nil
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch k {
		case "P":
			p.name = v
		case "V":
			p.version = v
		case "o":
			p.origin = v
		case "t":
			s, _ := strconv.ParseInt(v, 10, 64)
			p.built = time.Unix(s, 0)
		case "D":
			p.deps = strings.Fields(v)
		case "p":
			provides = strings.Fields(v)
		}
	}
	flush()
	return sc.Err()
}

// dep strips a dependency's version constraint; conflicts ("!x") are not dependencies.
func dep(d string) (string, bool) {
	if strings.HasPrefix(d, "!") {
		return "", false
	}
	i := strings.IndexAny(d, "<>=~")
	if i > 0 {
		d = d[:i]
	}
	return d, true
}

func (ix *index) closure(roots []string) (map[string]pkg, []string) {
	seen := map[string]pkg{}
	var unresolved []string
	var walk func(string)
	walk = func(name string) {
		if skipped(name) {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		p, ok := ix.pkgs[name]
		if !ok {
			unresolved = append(unresolved, name)
			return
		}
		seen[name] = p
		for _, d := range p.deps {
			d, ok := dep(d)
			if !ok {
				continue
			}
			prov, ok := ix.providers[d]
			if !ok {
				unresolved = append(unresolved, d)
				continue
			}
			walk(prov)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return seen, unresolved
}

type wolfi struct {
	names   map[string]pkg // lower-cased name
	origins map[string]pkg // lower-cased origin, newest build
	ix      *index
}

func (w wolfi) find(p pkg) (pkg, bool) {
	if r, ok := replaced[p.name]; ok {
		return w.find(pkg{name: r, origin: r})
	}
	if q, ok := w.names[strings.ToLower(p.name)]; ok {
		return q, true
	}
	if q, ok := w.origins[strings.ToLower(p.origin)]; ok {
		return q, true
	}
	// Named differently (python3 is python-3.x, gtk4.0 is gtk-4): the same library or command.
	for _, pr := range p.provides {
		k, _, _ := strings.Cut(pr, "=")
		if !strings.HasPrefix(k, "so:") && !strings.HasPrefix(k, "cmd:") && !strings.HasPrefix(k, "pc:") {
			continue
		}
		if name, ok := w.ix.providers[k]; ok {
			return w.ix.pkgs[name], true
		}
	}
	return pkg{}, false
}

func main() {
	stale := flag.Duration("stale", 180*24*time.Hour, "a Wolfi package not rebuilt for this long counts as abandoned")
	flag.Parse()

	alp, err := load(alpineMain, alpineComm)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	wix, err := load(wolfiIndex)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	w := wolfi{names: map[string]pkg{}, origins: map[string]pkg{}, ix: wix}
	for _, p := range wix.pkgs {
		w.names[strings.ToLower(p.name)] = p
		o := strings.ToLower(p.origin)
		if old, ok := w.origins[o]; !ok || p.built.After(old.built) {
			w.origins[o] = p
		}
	}

	now := time.Now()
	set, unresolved := alp.closure(desktop)
	// One row per Alpine origin: a source package is one recipe, however many subpackages.
	type row struct {
		origin string
		wolfi  pkg
		found  bool
	}
	rows := map[string]*row{}
	for _, p := range set {
		r, ok := rows[p.origin]
		if !ok {
			r = &row{origin: p.origin}
			rows[p.origin] = r
		}
		if q, ok := w.find(p); ok && (!r.found || q.built.After(r.wolfi.built)) {
			r.wolfi, r.found = q, true
		}
	}
	var have, old, missing []string
	for _, r := range rows {
		switch {
		case !r.found:
			missing = append(missing, r.origin)
		case now.Sub(r.wolfi.built) > *stale:
			old = append(old, fmt.Sprintf("%s(%s %s)", r.origin, r.wolfi.version, r.wolfi.built.Format("2006-01")))
		default:
			have = append(have, r.origin)
		}
	}
	slices.Sort(missing)
	slices.Sort(old)
	slices.Sort(unresolved)
	fmt.Printf("== GNOME: %d packages from %d source packages\n", len(set), len(rows))
	fmt.Printf("   in Wolfi and maintained: %d\n", len(have))
	fmt.Printf("   in Wolfi, not rebuilt for %v: %d\n     %s\n", *stale, len(old), strings.Join(old, " "))
	fmt.Printf("   not in Wolfi (recipes to own): %d\n     %s\n", len(missing), strings.Join(missing, " "))
	if len(unresolved) > 0 {
		fmt.Printf("   unresolved in Alpine: %s\n", strings.Join(slices.Compact(unresolved), " "))
	}
	fmt.Println()
}
