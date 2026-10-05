package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The theme's inner loop. The image is the source of truth - a theme lands through its fork's
// pin, the rootfs and a signed stick - but that path takes half an hour for a CSS change. dev-up
// boots the test stick once and leaves it running; theme-dev builds the local Orchis checkout,
// puts it in the live user's ~/.themes, makes GNOME Shell load it and the GTK themes follow
// (construct theme), and takes the shell's views against the needles: about a minute.

// devVM is where dev-up's machine answers: _output/dev/vm.json.
type devVM struct {
	CID   int    `json:"cid"`
	QMP   string `json:"qmp"`
	Scale int    `json:"scale"`
}

// devDir holds dev-up's machine - its stick, variables, QMP socket and serial log - and devCID is
// its vsock address: none of them a test's, so a test runs while it is up.
var (
	devDir   = filepath.Join(out, "dev")
	devState = filepath.Join(devDir, "vm.json")
)

const devCID = vsockCID + 1

// devUp boots the test stick (task usb -- -test) and keeps it until it is told to stop: SIGINT,
// SIGTERM or task dev:down.
func devUp(args []string) error {
	fl := flag.NewFlagSet("dev-up", flag.ExitOnError)
	mem := fl.String("m", vmMemory, "memory")
	scale := fl.Int("scale", 2, "2: the X9-15's 2880x1800 panel at 200%; 1: 1280x800 at 100%")
	_ = fl.Parse(args)
	sc, err := screenFor(*scale)
	if err != nil {
		return err
	}
	_, pub, err := testKey()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		return err
	}
	m, err := machine{mem: *mem, pubkey: pub, dir: devDir, cid: devCID, screen: sc}.withStick(devDir)
	if err != nil {
		return err
	}
	v, g, err := m.bootGuest(4 * time.Minute)
	if err != nil {
		return err
	}
	defer v.stop()
	if err := g.waitShell(3 * time.Minute); err != nil {
		return fmt.Errorf("GNOME Shell never answered Eval: %w", err)
	}
	// As the harness does: no blanking, and the installer the live session opens closed.
	_, _ = g.run(asLive+`gsettings set org.gnome.desktop.session idle-delay 0`, "")
	// The installer autostarts a few seconds after the login: it is waited for before it is
	// closed, or it opens over the views a moment later.
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if n, _ := g.eval(`String(` + installerWindows + `.length)`); strings.Trim(n, `"`) != "0" {
			break
		}
	}
	_, _ = g.eval(installerWindows + `.forEach(w => w.delete(global.get_current_time())); true`)
	w, h := sc.logical()
	if out, err := g.setSessionScale(float64(sc.Scale), w, h); err != nil {
		return fmt.Errorf("the session at %d00%%: %w: %s", sc.Scale, err, lastLines(out, 12))
	}
	b, err := json.Marshal(devVM{CID: v.cid, QMP: v.qmp, Scale: sc.Scale})
	if err != nil {
		return err
	}
	if err := os.WriteFile(devState, b, 0o644); err != nil {
		return err
	}
	defer os.Remove(devState)
	fmt.Println("up: task theme:dev builds the local Orchis into it, task type:dev measures its text, task dev:down stops it")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	exited := make(chan error, 1)
	go func() { exited <- v.cmd.Wait() }()
	select {
	case <-stop:
		return nil
	case err := <-exited:
		// dev:down quits QEMU over QMP, which exits 0: anything else stopped it.
		if err != nil {
			return fmt.Errorf("the machine stopped: %w", err)
		}
		return nil
	}
}

// devDown stops dev-up's machine.
func devDown([]string) error {
	d, err := readDevVM()
	if err != nil {
		return err
	}
	return qmpRun(d.QMP, map[string]any{"execute": "quit"})
}

func readDevVM() (devVM, error) {
	var d devVM
	b, err := os.ReadFile(devState)
	if errors.Is(err, fs.ErrNotExist) {
		return d, errors.New("no machine up: task dev:up first")
	}
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal(b, &d)
}

// liveThemes is where theme-dev puts the themes in the guest: GTK reads ~/.themes before
// /usr/share/themes, and construct theme copies GTK 4's from there.
const liveThemes = "/var/home/live/.themes"

// themeDev builds the Orchis checkout, puts it in the running machine and takes the shell's views.
func themeDev(args []string) error {
	fl := flag.NewFlagSet("theme-dev", flag.ExitOnError)
	orchis := fl.String("orchis", "", "the Orchis-theme checkout to build (ORCHIS)")
	needles := fl.String("needles", "", "the needles checkout to compare with (NEEDLES)")
	withType := fl.Bool("type", false, "take the type specimen and its rendering matrix too (_output/test/type.html)")
	_ = fl.Parse(args)
	if *orchis == "" {
		return errors.New("theme-dev: -orchis names an Orchis-theme checkout (ORCHIS)")
	}
	// The Wolfi base every build is pinned to (build/versions.env), not whatever :latest is today.
	base := os.Getenv("WOLFI_BASE_IMAGE")
	if base == "" {
		return errors.New("theme-dev: WOLFI_BASE_IMAGE unset: run it through task theme:dev")
	}
	d, err := readDevVM()
	if err != nil {
		return err
	}
	v, g := &vm{qmp: d.QMP, cid: d.CID}, &guest{cid: d.CID}

	built := filepath.Join(devDir, "themes")
	if err := os.RemoveAll(built); err != nil {
		return err
	}
	if err := os.MkdirAll(built, 0o755); err != nil {
		return err
	}
	src, err := filepath.Abs(*orchis)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(built)
	if err != nil {
		return err
	}
	start := time.Now()
	// As recipes/orchis-theme.yaml builds it: install.sh, light and dark, with sassc.
	build := exec.Command("docker", "run", "--rm", "-v", src+":/src:ro", "-v", abs+":/out",
		base, "sh", "-c",
		fmt.Sprintf(`apk add -q bash coreutils sassc >/dev/null && cp -r /src /w && cd /w && ./install.sh -d /out -c light dark >/dev/null && chown -R %d:%d /out`, os.Getuid(), os.Getgid()))
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("building %s: %w", src, err)
	}
	archive, err := tarGz(built)
	if err != nil {
		return err
	}
	if out, err := g.run(`base64 -d | `+asLive+`sh -c 'rm -rf `+liveThemes+` && mkdir -p `+liveThemes+` && tar -xz -C `+liveThemes+`' && `+
		asLive+`construct theme --themes `+liveThemes, archive); err != nil {
		return fmt.Errorf("putting the themes in the machine: %w: %s", err, out)
	}
	css := liveThemes + "/Orchis-Construct-Dark/gnome-shell/gnome-shell.css"
	if _, err := g.eval(`Main.setThemeStylesheet('` + css + `'); Main.loadTheme(); true`); err != nil {
		return fmt.Errorf("loading %s in GNOME Shell: %w", css, err)
	}
	fmt.Printf("built and loaded in %s\n", time.Since(start).Round(time.Second))

	dir := filepath.Join(out, "test")
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	r := &report{dir: dir, user: "live", scale: d.Scale}
	time.Sleep(time.Second)
	l := newLF(r, v, g)
	if err := g.fixClock(); err != nil {
		r.add("views' clock", false, "at "+viewsTime+": "+err.Error())
	}
	r.shellViews(v, g, l)
	if *withType {
		r.typeMatrix(v, g, l)
	}
	r.compareNeedles(*needles)
	return r.write(nil)
}

// typeDev takes the type specimen and its rendering matrix in dev-up's machine, and nothing else:
// the loop for a change to how text is drawn, about a minute.
func typeDev(args []string) error {
	fl := flag.NewFlagSet("type-dev", flag.ExitOnError)
	_ = fl.Parse(args)
	d, err := readDevVM()
	if err != nil {
		return err
	}
	v, g := &vm{qmp: d.QMP, cid: d.CID}, &guest{cid: d.CID}
	dir := filepath.Join(out, "test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Only the type's own files go: the rest of the last run's views stay beside them.
	for _, old := range []string{"type", "type.html", "type.json", "type-specimen.png", "type-specimen.json"} {
		if err := os.RemoveAll(filepath.Join(dir, old)); err != nil {
			return err
		}
	}
	r := &report{dir: dir, user: "live", scale: d.Scale}
	r.typeMatrix(v, g, newLF(r, v, g))
	return r.write(nil)
}

// tarGz is dir as a gzip'd tar, base64-encoded for an ssh session's standard input.
func tarGz(dir string) (string, error) {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	tw := tar.NewWriter(zw)
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		if h.Name, err = filepath.Rel(dir, p); err != nil {
			return err
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			_, err = tw.Write(f)
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b.Bytes()), nil
}
