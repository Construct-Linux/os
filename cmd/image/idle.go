package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// idleWindow is how long the session is left alone to be measured at rest.
const idleWindow = 30

// idleScript measures the session at rest, as root in the guest: every process's CPU ticks and
// scheduler runs over the window, printed as shares and rates with the busiest first, and the
// shell's own line first of all, for the report. The shell is the session's: the greeter's has
// exited by now.
var idleScript = `snap() { for s in /proc/[0-9]*/stat; do p=${s%/stat}; runs=$(cut -d' ' -f3 "$p/schedstat" 2>/dev/null) || continue; ` +
	`awk -v pid="${p#/proc/}" -v runs="$runs" '{ n = split($0, a, ")"); split(a[n], f, " "); ` +
	`comm = substr($0, index($0, "(") + 1, index($0, ")") - index($0, "(") - 1); print pid, comm, f[12] + f[13], runs }' "$s" 2>/dev/null; done; }; ` +
	`snap | sort > /tmp/idle1; sleep ` + fmt.Sprint(idleWindow) + `; snap | sort > /tmp/idle2; ` +
	`join /tmp/idle1 /tmp/idle2 | awk -v secs=` + fmt.Sprint(idleWindow) + ` '$2 == $5 { printf "%6.2f%% %7.1f/s  %s[%s]\n", ($6-$3)/secs, ($7-$4)/secs, $2, $1 }' | sort -rn > /tmp/idle; ` +
	`grep -E ' gnome-shell\[' /tmp/idle | head -1; echo; head -20 /tmp/idle; ` +
	// The busiest process, when it is not the shell: its threads, where each waits and its
	// kernel stack, and the files it holds, which say what it is doing without a debugger.
	`p=$(head -1 /tmp/idle | sed -E 's/.*\[([0-9]+)\]$/\1/'); if [ -n "$p" ] && ! head -1 /tmp/idle | grep -q ' gnome-shell\['; then ` +
	`echo; echo "the busiest, $p:"; for t in /proc/$p/task/*; do echo "  $(cat $t/comm) in $(cat $t/wchan 2>/dev/null)"; sed 's/^/    /' $t/stack 2>/dev/null | head -6; done | head -80; ` +
	`echo "  files:"; ls -l /proc/$p/fd 2>/dev/null | awk '{print $NF}' | grep -vE '^(socket|anon_inode|pipe)' | sort | uniq -c | sort -rn | head -12 | sed 's/^/    /'; fi`

// idle measures the session at rest for idleWindow seconds: the shell's CPU share and how often
// the scheduler ran it - every timer an extension keeps is a wakeup - and the busiest processes,
// in idle.txt. Not a gate, as shell-startup.txt is not: a desktop whose shell polls at rest shows
// here as runs per second, tens of them, where a quiet one has a few.
func (r *report) idle(g *guest) {
	out, err := g.run(idleScript, "")
	if err != nil {
		r.add("session at rest", false, err.Error())
		return
	}
	_ = os.WriteFile(filepath.Join(r.dir, "idle.txt"), []byte(strings.TrimLeft(out, "\n")), 0o644)
	shell := firstLine(out)
	detail := fmt.Sprintf("the shell over %d s: %s", idleWindow, oneLine(shell))
	// Whatever else is busy on a desktop nobody is using, named in the report rather than in a
	// file nobody opens: gnome-software's first refresh of Flathub was, at a quarter of a core.
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) > 2 && !strings.Contains(lines[2], "gnome-shell[") {
		detail += "; the busiest " + oneLine(lines[2])
	}
	r.add("session at rest", strings.Contains(shell, "gnome-shell["), detail+"; idle.txt")
}
