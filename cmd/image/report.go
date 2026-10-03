package main

import (
	"bufio"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type result struct {
	name, detail string
	ok           bool
	// skipped is a check this machine cannot run, said so rather than passed.
	skipped bool
}

type report struct {
	dir string
	// user is whose session the user-unit check reads: live on the test disk; none on the
	// lifecycle's installed machine, where GDM waits for the person's password.
	user    string
	results []result
}

func (r *report) add(name string, ok bool, detail string) {
	r.results = append(r.results, result{name: name, detail: detail, ok: ok})
	mark := "PASS"
	if !ok {
		mark = "FAIL"
	}
	fmt.Printf("%s %-36s %s\n", mark, name, detail)
}

// skip records a check that could not run here, and why: never a pass, never silent.
func (r *report) skip(name, why string) {
	r.results = append(r.results, result{name: name, detail: why, ok: true, skipped: true})
	fmt.Printf("SKIP %-36s %s\n", name, why)
}

// shot saves the screen as name.png; one it cannot take is a failure of its own, since the report
// names the file for a person to look at.
func (r *report) shot(v *vm, name string) {
	ppm := filepath.Join(r.dir, name+".ppm")
	err := v.screenshot(ppm)
	if err == nil {
		time.Sleep(500 * time.Millisecond)
		err = ppmToPNG(ppm, strings.TrimSuffix(ppm, ".ppm")+".png")
	}
	if err != nil {
		r.add("screenshot "+name, false, err.Error())
		return
	}
	_ = os.Remove(ppm)
}

// write adds what the system says about itself - failed units, core dumps, the journal's errors
// - and writes the report; it fails when any scenario or system check did.
func (r *report) write(g *guest) error {
	if g != nil {
		r.system(g)
	}
	var b strings.Builder
	bad, skipped := 0, 0
	for _, x := range r.results {
		mark := "PASS"
		switch {
		case x.skipped:
			mark = "SKIP"
			skipped++
		case !x.ok:
			mark = "FAIL"
			bad++
		}
		fmt.Fprintf(&b, "%s %s: %s\n", mark, x.name, x.detail)
	}
	if err := os.WriteFile(filepath.Join(r.dir, "report.txt"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	ran := len(r.results) - skipped
	fmt.Printf("%d/%d passed, %d skipped; screenshots and journal-errors.txt in %s\n", ran-bad, ran, skipped, r.dir)
	if bad > 0 {
		return fmt.Errorf("%d checks failed", bad)
	}
	return nil
}

// system adds what the machine says about itself: failed units, core dumps, the journal's errors.
//
// Each check passes only on an answer: a guest that no longer answers over SSH says nothing, which
// is not that nothing failed.
func (r *report) system(g *guest) {
	failed, failedErr := g.run(`systemctl --failed --no-legend --plain | awk '{print $1}'`, "")
	// EXE is the next-to-last column; the last is the core's size.
	dumps, dumpsErr := g.run(`coredumpctl --no-pager --no-legend list 2>/dev/null | awk '{print $(NF-1)}' | sort | uniq -c`, "")
	if strings.TrimSpace(dumps) != "" {
		info, _ := g.run(`coredumpctl --no-pager info 2>&1`, "")
		_ = os.WriteFile(filepath.Join(r.dir, "coredumps.txt"), []byte(info), 0o644)
	}
	errs, errsErr := g.run(`journalctl -b -p err -q --no-pager -o short-monotonic`, "")
	// What GNOME Shell logs below error priority but is a bug all the same: an extension's
	// exception, an actor used after it was destroyed (both typical of a lock/unlock cycle, which
	// disables and enables every extension), a cursor the theme lacks.
	shell, shellErr := g.run(`journalctl -b --no-pager -o cat _COMM=gnome-shell | grep -E 'JS ERROR|already disposed|Could not load cursor' || true`, "")
	_ = os.WriteFile(filepath.Join(r.dir, "shell-log.txt"), []byte(shell), 0o644)
	lines := strings.Fields(strings.ReplaceAll(strings.TrimSpace(shell), " ", "_"))
	r.add("shell log", shellErr == nil && len(lines) == 0, fmt.Sprintf("%d lines in shell-log.txt: %s", len(lines), oneLine(firstLine(shell)))+errText(shellErr))
	r.add("system units", failedErr == nil && strings.TrimSpace(failed) == "", "failed: "+oneLine(failed)+errText(failedErr))
	// Each failed unit's own log, while the machine that has it is still up.
	units := filepath.Join(r.dir, "units")
	for _, u := range strings.Fields(failed) {
		log, _ := g.run("journalctl -b --no-pager -n 60 -u "+u, "")
		_ = os.MkdirAll(units, 0o755)
		_ = os.WriteFile(filepath.Join(units, u+".log"), []byte(log), 0o644)
	}
	if r.user != "" {
		userFailed, err := g.run(`set -o pipefail; systemctl --user -M `+r.user+`@ --failed --no-legend --plain | awk '{print $1}'`, "")
		r.add("user units", err == nil && strings.TrimSpace(userFailed) == "", "failed: "+oneLine(userFailed)+errText(err))
	}
	r.add("core dumps", dumpsErr == nil && strings.TrimSpace(dumps) == "", oneLine(dumps)+errText(dumpsErr))
	// A PAM file that includes one the image does not have fails every authentication through it,
	// and says so only in the journal.
	pam, pamErr := g.run(`journalctl -b --no-pager -o cat | grep -E 'unable to open config for' | sort -u || true`, "")
	r.add("pam", pamErr == nil && strings.TrimSpace(pam) == "", oneLine(pam)+errText(pamErr))
	_ = os.WriteFile(filepath.Join(r.dir, "journal-errors.txt"), []byte(errs), 0o644)
	// An error in the journal is a fault of the image's, whoever logged it: none is expected. -q, or
	// an empty journal reads "-- No entries --".
	errLines := strings.Count(strings.TrimSpace(errs), "\n")
	if strings.TrimSpace(errs) != "" {
		errLines++
	}
	r.add("journal errors", errsErr == nil && errLines == 0, fmt.Sprintf("%d in journal-errors.txt: %s", errLines, oneLine(firstLine(errs)))+errText(errsErr))
	// Where the boot's time went, for every build: the total in the report, the chain to the
	// desktop and the slowest units in boot.txt. Not a gate - a VM's numbers are not a machine's -
	// but a baseline to compare builds by.
	boot, bootErr := g.run(`systemd-analyze time; echo; systemd-analyze critical-chain graphical.target; echo; systemd-analyze blame | head -30`, "")
	_ = os.WriteFile(filepath.Join(r.dir, "boot.txt"), []byte(boot), 0o644)
	// The initrd's own timeline, which systemd-analyze's chain does not show: on a stick's first
	// boot it makes /var, and where that time goes is in its journal.
	initrd, _ := g.run(`journalctl -b -o short-monotonic --no-pager | sed '/Switching root/q'`, "")
	_ = os.WriteFile(filepath.Join(r.dir, "initrd.txt"), []byte(initrd), 0o644)
	r.add("boot time", bootErr == nil, oneLine(firstLine(boot))+errText(bootErr))
	// Plymouth's own log, when the disk was built to boot with plymouth.debug.
	if ply, err := g.run("cat /var/log/plymouth-debug.log", ""); err == nil && ply != "" {
		_ = os.WriteFile(filepath.Join(r.dir, "plymouth-debug.log"), []byte(ply), 0o644)
	}

}

func firstLine(s string) string {
	return strings.SplitN(strings.TrimSpace(s), "\n", 2)[0]
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}

// ppmToPNG converts QEMU's screendump (binary P6, 8-bit) to PNG.
func ppmToPNG(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	var fields []int
	var magic string
	for len(fields) < 3 {
		tok, err := ppmToken(br)
		if err != nil {
			return err
		}
		if magic == "" {
			magic = tok
			continue
		}
		n, err := strconv.Atoi(tok)
		if err != nil {
			return err
		}
		fields = append(fields, n)
	}
	if magic != "P6" || fields[2] != 255 {
		return fmt.Errorf("%s: not an 8-bit P6", src)
	}
	w, h := fields[0], fields[1]
	pix := make([]byte, w*h*3)
	if _, err := io.ReadFull(br, pix); err != nil {
		return err
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Set(i%w, i/w, color.RGBA{pix[3*i], pix[3*i+1], pix[3*i+2], 255})
	}
	o, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := png.Encode(o, img); err != nil {
		o.Close()
		return err
	}
	return o.Close()
}

// ppmToken reads one whitespace-separated header token, and the single whitespace after it.
func ppmToken(br *bufio.Reader) (string, error) {
	var tok []byte
	for {
		c, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if c == '#' {
			_, _ = br.ReadString('\n')
			continue
		}
		if c == ' ' || c == '\n' || c == '\t' || c == '\r' {
			if len(tok) > 0 {
				return string(tok), nil
			}
			continue
		}
		tok = append(tok, c)
	}
}
