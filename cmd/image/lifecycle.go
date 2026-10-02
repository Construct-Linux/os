package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// lifecycle is a machine's life with Spindrift, in QEMU: installed from a stick onto its NVMe
// disk, booted from it, updated by plugging in the stick of the next release, and rolled back by
// systemd-boot from a release whose desktop never comes up - with what the person kept in /var and
// changed in /etc there all along. The sticks are three test builds, oldest first: task
// test:lifecycle makes them.
func lifecycle(args []string) error {
	fl := flag.NewFlagSet("lifecycle", flag.ExitOnError)
	dir := fl.String("dir", filepath.Join(out, "lifecycle"), "where v1.raw, v2.raw and v3.raw (broken) are, and the machine's disk and variables go")
	_ = fl.Parse(args)

	versions := map[string]string{}
	for _, v := range []string{"v1", "v2", "v3"} {
		b, err := os.ReadFile(filepath.Join(*dir, v+".version"))
		if err != nil {
			return err
		}
		versions[v] = strings.TrimSpace(string(b))
	}
	_, pub, err := testKey()
	if err != nil {
		return err
	}
	nvme, vars := filepath.Join(*dir, "nvme.raw"), filepath.Join(*dir, "OVMF_VARS.fd")
	_ = os.Remove(nvme)
	if err := copyCmd("truncate", "-s", "64G", nvme); err != nil {
		return err
	}
	if err := freshVars(vars); err != nil {
		return err
	}
	stick := func(v string) (string, error) {
		s := filepath.Join(*dir, v+".stick")
		return s, stickCopy(filepath.Join(*dir, v+".raw"), s)
	}
	r := &report{dir: *dir}
	boot := func(sticks ...string) (*vm, *guest, error) {
		return machine{mem: "6G", pubkey: pub, vars: vars, sticks: sticks, nvme: nvme}.bootGuest(5 * time.Minute)
	}

	// 1. The live stick installs onto the NVMe disk.
	s1, err := stick("v1")
	if err != nil {
		return err
	}
	v, g, err := boot(s1)
	if err != nil {
		return err
	}
	start := time.Now()
	// As a person installs: drift reads the release from the stick it runs from. The person is
	// given as the installer would be told; the password goes through a file, as --yes asks nothing.
	outs, err := g.run(`set -e; printf '%s\n' '`+lifecyclePassword+`' > /run/pw; `+
		`drift install --disk /dev/nvme0n1 --yes --user `+lifecycleUser+` --real-name 'Ada Lovelace' --password-file /run/pw; rm /run/pw`, "")
	_ = os.WriteFile(filepath.Join(*dir, "install.txt"), []byte(outs), 0o644)
	// How long an install takes is part of what it is: the person waits on it.
	r.add("install", err == nil, fmt.Sprintf("%s in %s%s", lastLine(outs), time.Since(start).Round(time.Second), errText(err)))
	v.powerOff(g)
	if err != nil {
		return r.write(nil)
	}

	// 2. The machine boots its disk: v1, /var on the NVMe, and what the person keeps.
	v, g, err = boot()
	if err != nil {
		r.add("boot the disk", false, err.Error())
		return r.write(nil)
	}
	r.add("boot the disk", g.version() == versions["v1"], "running "+g.version())
	src, _ := g.run(`awk '$2=="/var"{print $1}' /proc/mounts`, "")
	r.add("/var on the disk", strings.Contains(src, "nvme0n1"), oneLine(src))
	_, err = g.run(`echo kept > /var/home/`+lifecycleUser+`/marker && echo kept > /etc/spindrift-marker`, "")
	r.add("keep a file in /var and one in /etc", err == nil, errText(err))
	r.person(g)
	ok, detail := g.waitUnit2Active("boot-complete.target", 5*time.Minute)
	r.add("v1 comes up", ok, detail)
	v.powerOff(g)

	// 3. Sticks that are not a release its key signed are refused, and change nothing.
	for _, bad := range []string{"unsigned", "badsig", "sums", "uki"} {
		s, err := stick("v2-" + bad)
		if err != nil {
			return err
		}
		if v, g, err = boot(s); err != nil {
			r.add("refuse "+bad, false, err.Error())
			return r.write(nil)
		}
		u := g.waitUpdate(15 * time.Minute)
		esp, _ := g.run(`ls /boot/EFI/Linux/`, "")
		// Refused by systemd-sysupdate itself: a unit that never ran, or failed before sysupdate
		// read the stick (its mount), refused nothing.
		refused := u.ended && !u.ok && u.sysupdate != "" && !strings.Contains(esp, versions["v2"]) && g.version() == versions["v1"]
		r.add("refuse a stick: "+bad, refused, u.detail()+"; ESP: "+oneLine(esp))
		v.powerOff(g)
	}

	// 4. v2 comes from where releases are published - an HTTP server on this host here, which the
	// default component's transfers are pointed at - by the daily timer's service.
	srv, url, err := serve(filepath.Join(*dir, "v2.release"))
	if err != nil {
		return err
	}
	defer srv.Close()
	if v, g, err = boot(); err != nil {
		r.add("boot for the URL update", false, err.Error())
		return r.write(nil)
	}
	_, err = g.run(`mkdir -p /etc/sysupdate.d && for f in /usr/lib/sysupdate.d/*.transfer; do `+
		`sed "s|^Path=https://.*|Path=`+url+`|" "$f" > /etc/sysupdate.d/$(basename "$f"); done`, "")
	r.add("point updates at the test server", err == nil, url+errText(err))
	_, err = g.run(`systemctl start spindrift-update.service`, "")
	outs, _ = g.run(`journalctl -b --no-pager -o cat -u spindrift-update.service | tail -3`, "")
	r.add("v2 downloads", err == nil, oneLine(outs)+errText(err))
	esp, _ := g.run(`ls /boot/EFI/Linux/`, "")
	r.add("v2 is counted on the ESP", strings.Contains(esp, "spindrift_"+versions["v2"]+"+3"), oneLine(esp))
	mark, _ := g.run(`cat /run/spindrift/reboot-required`, "")
	r.add("the person is told to reboot", strings.TrimSpace(mark) == versions["v2"], "reboot-required: "+oneLine(mark))
	g.reboot()
	if err := g.wait(5 * time.Minute); err != nil {
		r.add("reboot into v2", false, err.Error())
		v.stop()
		return r.write(nil)
	}
	r.add("reboot into v2", g.version() == versions["v2"], "running "+g.version())
	kept, _ := g.run(`cat /var/home/`+lifecycleUser+`/marker /etc/spindrift-marker`, "")
	r.add("kept across the update", strings.Count(kept, "kept") == 2, oneLine(kept))
	ok, detail = g.waitUnit2Active("boot-complete.target", 5*time.Minute)
	r.add("v2 comes up", ok, detail)
	time.Sleep(10 * time.Second) // systemd-bless-boot runs once the target is reached
	esp, _ = g.run(`ls /boot/EFI/Linux/`, "")
	r.add("v2 is blessed", strings.Contains(esp, "spindrift_"+versions["v2"]+".efi"), oneLine(esp))
	v.powerOff(g)

	// 5. v3's desktop never comes up: three tries, then systemd-boot falls back to v2 by itself.
	s3, err := stick("v3")
	if err != nil {
		return err
	}
	if v, g, err = boot(s3); err != nil {
		r.add("boot with v3's stick", false, err.Error())
		return r.write(nil)
	}
	u := g.waitUpdate(15 * time.Minute)
	r.add("v3's stick updates", u.ok, u.detail())
	g.reboot()
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		if g.wait(30*time.Second) == nil && g.version() == versions["v2"] && g.uptimeOver(20*time.Second) {
			break
		}
		time.Sleep(10 * time.Second)
	}
	r.add("v3 rolls back to v2", g.version() == versions["v2"], "running "+g.version())
	esp, _ = g.run(`ls /boot/EFI/Linux/`, "")
	r.add("v3 is marked bad", strings.Contains(esp, "spindrift_"+versions["v3"]+"+0-3"), oneLine(esp))
	kept, _ = g.run(`cat /var/home/`+lifecycleUser+`/marker /etc/spindrift-marker`, "")
	r.add("kept across the rollback", strings.Count(kept, "kept") == 2, oneLine(kept))
	err = r.write(g)
	v.powerOff(g)
	return err
}

// updateEnd is how the spindrift-update@ unit a plugged stick started ended: whether it did, and
// well, systemd's line for it, and what systemd-sysupdate last said.
type updateEnd struct {
	ended, ok       bool
	unit, sysupdate string
}

func (u updateEnd) detail() string {
	if !u.ended {
		return "no spindrift-update@ unit ended"
	}
	return u.unit + ": systemd-sysupdate: " + oneLine(u.sysupdate)
}

// waitUpdate waits for the spindrift-update@ unit a plugged stick started to end. It reads the
// journal: a oneshot that already ended is unloaded, and no longer listed.
func (g *guest) waitUpdate(d time.Duration) updateEnd {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		log, _ := g.run(`journalctl -b --no-pager -o cat -u 'spindrift-update@*' | grep -E '^(Finished|Failed to start) Update' | tail -1`, "")
		if log = strings.TrimSpace(log); log != "" {
			tail, _ := g.run(`journalctl -b --no-pager -o cat -u 'spindrift-update@*' -t systemd-sysupdate | tail -3`, "")
			return updateEnd{ended: true, ok: strings.HasPrefix(log, "Finished"), unit: log, sysupdate: strings.TrimSpace(tail)}
		}
		time.Sleep(3 * time.Second)
	}
	return updateEnd{}
}

// serve answers dir's files over HTTP on this host, where a guest reaches it at 10.0.2.2 (QEMU's
// user network): what GitHub Releases is to a machine.
func serve(dir string) (*http.Server, string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	srv := &http.Server{Handler: http.FileServer(http.Dir(dir))}
	go func() { _ = srv.Serve(l) }()
	return srv, fmt.Sprintf("http://10.0.2.2:%d", l.Addr().(*net.TCPAddr).Port), nil
}

// The person test:lifecycle installs for.
const (
	lifecycleUser     = "ada"
	lifecyclePassword = "correct horse battery staple"
)

// person checks who an installed machine is for: GDM asks for their password, there is no live
// user, and sudo asks for their password and takes it.
func (r *report) person(g *guest) {
	sessions, _ := g.run(`sleep 20; loginctl list-sessions --no-legend | awk '{print $3}' | sort -u | tr '\n' ' '`, "")
	// GDM asks for the person's password: nobody is logged in at boot, and there is no live user.
	r.add("asked for a password at boot", !strings.Contains(sessions, lifecycleUser) && !strings.Contains(sessions, "live") && strings.Contains(sessions, "gdm"), "sessions: "+sessions)
	noPw, _ := g.run(`sudo -u `+lifecycleUser+` sudo -n true 2>&1; echo rc=$?`, "")
	r.add("sudo asks for the password", strings.Contains(noPw, "rc=1"), oneLine(noPw))
	withPw, _ := g.run(`printf '%s\n' '`+lifecyclePassword+`' | sudo -u `+lifecycleUser+` sudo -S -p '' id -u 2>&1; echo rc=$?`, "")
	r.add("sudo takes the password", strings.Contains(withPw, "0\nrc=0") || strings.HasPrefix(strings.TrimSpace(withPw), "0"), oneLine(withPw))
	// The person changes their own password, where Settings and passwd change it (pam_unix,
	// /etc/shadow), and sudo then takes the new one.
	const newPassword = "quiet lantern harbor seven"
	changed, err := g.run(`printf '%s\n%s\n%s\n' '`+lifecyclePassword+`' '`+newPassword+`' '`+newPassword+`' | runuser -u `+lifecycleUser+` -- passwd 2>&1; echo rc=$?`, "")
	r.add("the person changes their password", err == nil && strings.Contains(changed, "rc=0"), oneLine(changed))
	newPw, _ := g.run(`printf '%s\n' '`+newPassword+`' | sudo -u `+lifecycleUser+` sudo -k -S -p '' id -u 2>&1; echo rc=$?`, "")
	r.add("sudo takes the new password", strings.HasPrefix(strings.TrimSpace(newPw), "0"), oneLine(newPw))
	root, _ := g.run(`getent shadow root | cut -d: -f2`, "")
	r.add("root has no password", strings.TrimSpace(root) == "*", "root: "+strings.TrimSpace(root))
}
