package main

import (
	"bufio"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// scale is how many physical pixels a stage pixel is in its screenshots: 0 is 1.
	scale int
	// views are the specs of the views shot so far, by name: lf's checks count on them.
	views map[string]*needleSpec
	// benchWindow is when construct bench ran, in seconds since the guest's boot: the kernel's
	// out-of-memory report it provokes on purpose is not an error of the image's.
	benchWindow [2]float64
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
	if err := v.png(filepath.Join(r.dir, name+".png")); err != nil {
		r.add("screenshot "+name, false, err.Error())
	}
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
		// A crashed shell's JavaScript stack - which extension's code called into C last - is
		// logged as it dies, below error priority; the native stack alone stops at GJS.
		js, _ := g.run(`journalctl -b --no-pager -o cat _COMM=gnome-shell | grep -A80 'Stack trace for context' || true`, "")
		_ = os.WriteFile(filepath.Join(r.dir, "coredumps.txt"), []byte(js+"\n"+info), 0o644)
	}
	errs, errsErr := g.run(`journalctl -b -p err -q --no-pager -o short-monotonic`, "")
	// What GNOME Shell logs below error priority but is a bug all the same: an extension's
	// exception, an actor used after it was destroyed (both typical of a lock/unlock cycle, which
	// disables and enables every extension), a cursor the theme lacks, and St's CSS warnings - a
	// property the theme writes that the shell ignores ("at line L, col C" of its stylesheet).
	shell, shellErr := g.run(`journalctl -b --no-pager -o cat _COMM=gnome-shell | grep -E 'JS ERROR|already disposed|Could not load cursor|at line [0-9]+, col [0-9]+' || true`, "")
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
	errs = withoutBenchOOM(errs, r.benchWindow)
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

// ppmToPNG converts QEMU's screendump to PNG.
func ppmToPNG(src, dst string) error {
	img, err := decodePPM(src)
	if err != nil {
		return err
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

// decodePPM reads QEMU's screendump (binary P6, 8-bit).
func decodePPM(p string) (*image.RGBA, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	var fields []int
	magic := ""
	for len(fields) < 3 {
		tok, err := ppmToken(br)
		if err != nil {
			return nil, err
		}
		if magic == "" {
			magic = tok
			continue
		}
		n, err := strconv.Atoi(tok)
		if err != nil {
			return nil, err
		}
		fields = append(fields, n)
	}
	if magic != "P6" || fields[2] != 255 {
		return nil, fmt.Errorf("%s: not an 8-bit P6", p)
	}
	w, h := fields[0], fields[1]
	pix := make([]byte, w*h*3)
	if _, err := io.ReadFull(br, pix); err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range w * h {
		copy(img.Pix[4*i:4*i+3], pix[3*i:3*i+3])
		img.Pix[4*i+3] = 255
	}
	return img, nil
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

// withoutBenchOOM is the journal's errors (short-monotonic) without the kernel's lines while
// construct bench ran: its memory test asks for more than there is, and the kernel's report of
// the stress-ng worker it kills for that is the measurement, not a fault. Any other error then -
// a unit's, a service's - stays.
func withoutBenchOOM(errs string, window [2]float64) string {
	if window[1] == 0 {
		return errs
	}
	var kept []string
	for _, line := range strings.Split(errs, "\n") {
		var at float64
		if _, err := fmt.Sscanf(strings.TrimLeft(line, "[ "), "%f]", &at); err == nil &&
			at >= window[0] && at <= window[1] && strings.Contains(line, " kernel: ") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
