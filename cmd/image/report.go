package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Construct-Linux/os/internal/needle"
	"github.com/Construct-Linux/os/internal/qemu"
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
	views map[string]*needle.Spec
	// sweeps are C3's sweep of each view, for look.json.
	sweeps map[string]lookSweep
	// pal is the guest's palette once lf has read it: a view's noise counts its colours.
	pal *lfPalette
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

// guestFile copies the guest's file at path into the report as name; one it cannot copy is a
// failure, as a shot is.
func (r *report) guestFile(path, name string, g *guest) {
	out, err := g.run("base64 -w0 "+path, "")
	if err == nil {
		var b []byte
		if b, err = base64.StdEncoding.DecodeString(strings.TrimSpace(out)); err == nil {
			err = os.WriteFile(filepath.Join(r.dir, name), b, 0o644)
		}
	}
	if err != nil {
		r.add("file "+name, false, err.Error())
	}
}

// shot saves the screen as name.png; one it cannot take is a failure of its own, since the report
// names the file for a person to look at.
func (r *report) shot(v *qemu.VM, name string) {
	if err := v.PNG(filepath.Join(r.dir, name+".png")); err != nil {
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
	// What every error came after and with: an error line alone - a read cut short by a process
	// killed, say - does not name what was killed, and a failure only the release builder's
	// timing shows cannot be booted again to look.
	if strings.TrimSpace(errs) != "" {
		if all, err := g.run(`journalctl -b -q --no-pager -o short-monotonic`, ""); err == nil {
			_ = os.WriteFile(filepath.Join(r.dir, "journal-context.txt"), []byte(aroundErrors(all, errs, 5, 2)), 0o644)
		}
	}
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

// aroundErrors is journal's lines from before seconds ahead of each of errs' lines to after
// seconds past it, by their monotonic stamps, a "--" between windows apart.
func aroundErrors(journal, errs string, before, after float64) string {
	var at []float64
	for _, line := range strings.Split(errs, "\n") {
		if t, ok := monotonic(line); ok {
			at = append(at, t)
		}
	}
	var b strings.Builder
	in := false
	for _, line := range strings.Split(journal, "\n") {
		t, ok := monotonic(line)
		if !ok {
			continue
		}
		near := slices.ContainsFunc(at, func(e float64) bool { return t >= e-before && t <= e+after })
		if !near && in {
			b.WriteString("--\n")
		}
		if near {
			b.WriteString(line + "\n")
		}
		in = near
	}
	return b.String()
}

// monotonic is the seconds since boot a journalctl -o short-monotonic line starts with.
func monotonic(line string) (float64, bool) {
	var t float64
	_, err := fmt.Sscanf(strings.TrimLeft(line, "[ "), "%f]", &t)
	return t, err == nil
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
		if at, ok := monotonic(line); ok && at >= window[0] && at <= window[1] && strings.Contains(line, " kernel: ") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
