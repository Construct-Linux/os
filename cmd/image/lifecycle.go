package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	nvme, vars, tpm := filepath.Join(*dir, "nvme.raw"), filepath.Join(*dir, "OVMF_VARS.fd"), filepath.Join(*dir, "tpm")
	_ = os.Remove(nvme)
	if err := os.RemoveAll(tpm); err != nil {
		return err
	}
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
	// The machine has a TPM, and every boot after the first of its disk is handed the person's
	// password as the TPM's PIN, as if typed: systemd-cryptsetup asks for it through libcryptsetup's
	// token plugin, which reads cryptsetup.luks2-pin. No passphrase credential: a TPM that refuses
	// leaves the boot waiting, and the check fails.
	m := machine{mem: "6G", pubkey: pub, vars: vars, nvme: nvme, tpm: tpm,
		credentials: []string{"cryptsetup.luks2-pin=" + lifecyclePassword}}
	boot := func(sticks ...string) (*vm, *guest, error) {
		m := m
		m.sticks = sticks
		return m.bootGuest(5 * time.Minute)
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
	// As a person installs: the graphical installer's command, from the live session it runs in
	// - pkexec, which the session's polkit rule lets through, drift install --json, the password
	// on standard input - read from the stick it runs from.
	outs, err := g.installAsTheInstaller()
	_ = os.WriteFile(filepath.Join(*dir, "install.txt"), []byte(outs), 0o644)
	if err == nil && (!strings.Contains(outs, `"percent":100`) || !strings.Contains(outs, `"msg":"done"`)) {
		err = fmt.Errorf("no 100%% or done record")
	}
	// How long an install takes is part of what it is: the person waits on it.
	said := lastLine(outs)
	for _, line := range strings.Split(outs, "\n") {
		if strings.Contains(line, `"msg":"done"`) {
			said = line
		}
	}
	r.add("install", err == nil, fmt.Sprintf("%s in %s%s", oneLine(said), time.Since(start).Round(time.Second), errText(err)))
	r.add("a recovery key is shown", strings.Contains(said, `"recovery_key":"`) && !strings.Contains(said, `"recovery_key":""`), "")
	v.powerOff(g)
	if err != nil {
		return r.write(nil)
	}

	// 2. The machine boots its disk: the boot waits for the person's password, typed at the
	// prompt - the TPM's PIN - then v1, /var on the NVMe and encrypted, and what the person keeps.
	first := m
	first.credentials = nil
	if v, err = first.start(); err != nil {
		return err
	}
	g = &guest{cid: v.cid}
	asked := g.wait(45*time.Second) != nil
	_ = v.screenshot(filepath.Join(*dir, "password.ppm"))
	time.Sleep(500 * time.Millisecond)
	_ = ppmToPNG(filepath.Join(*dir, "password.ppm"), filepath.Join(*dir, "password.png"))
	r.add("the disk asks for the password", asked, "screen: password.png")
	err = v.typeText(lifecyclePassword)
	if err == nil {
		err = g.wait(5 * time.Minute)
	}
	r.add("the password opens the disk", err == nil, errText(err))
	if err != nil {
		v.stop()
		return r.write(nil)
	}
	r.add("boot the disk", g.version() == versions["v1"], "running "+g.version())
	r.secureBoot(g)
	src, _ := g.run(`awk '$2=="/var"{print $1}' /proc/mounts; cryptsetup status var | grep -E 'type|device'`, "")
	r.add("/var on the disk, encrypted", strings.Contains(src, "/dev/mapper/var") && strings.Contains(src, "LUKS2") && strings.Contains(src, "nvme0n1p6"), oneLine(src))
	slots, _ := g.run(`systemd-cryptenroll /dev/nvme0n1p6`, "")
	r.add("the TPM, the password and the recovery key open it", strings.Contains(slots, "tpm2") && strings.Contains(slots, "password") && strings.Contains(slots, "recovery"), oneLine(slots))
	// The PIN typed is cached for the passphrase prompt that follows a TPM refusal, so a boot that
	// came up says nothing: the unit's log does.
	unlocked, err := g.run(`systemctl is-active systemd-cryptsetup@var.service; journalctl -b -o cat -u systemd-cryptsetup@var.service | grep -iE 'unseal|falling back|failed' | tail -3`, "")
	r.add("the TPM unlocked it", err == nil && strings.HasPrefix(unlocked, "active") && strings.TrimSpace(strings.TrimPrefix(unlocked, "active")) == "", oneLine(unlocked))
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

	// 7. A factory reset, as systemd asks for one: the machine reboots, its /var made anew in the
	// initrd - the release kept, the person and what they kept gone - and GDM opens GNOME's
	// Initial Setup for the machine's new person.
	_, _ = g.run(`systemctl start --no-block factory-reset.target`, "")
	for down := time.Now().Add(3 * time.Minute); time.Now().Before(down); time.Sleep(time.Second) {
		if _, err := g.run("true", ""); err != nil {
			break
		}
	}
	err = g.wait(10 * time.Minute)
	r.add("factory reset reboots", err == nil && g.version() == versions["v2"], "running "+g.version()+errText(err))
	gone, _ := g.run(`ls /var/home/; getent passwd `+lifecycleUser+`; cat /etc/spindrift-marker 2>&1`, "")
	r.add("factory reset forgets the person", !strings.Contains(gone, lifecycleUser) && !strings.Contains(gone, "kept"), oneLine(gone))
	setup, _ := g.run(`for i in $(seq 60); do loginctl list-sessions --no-legend | grep -q gnome-initial-setup && break; sleep 2; done; loginctl list-sessions --no-legend | awk '{print $3}' | sort -u | tr '\n' ' '`, "")
	r.add("initial setup for the new person", strings.Contains(setup, "gnome-initial-setup"), "sessions: "+setup)
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

// person checks who an installed machine is for: logged in once their password opened /var, no live
// user, and sudo asks for their password and takes it.
func (r *report) person(g *guest) {
	sessions, _ := g.run(`sleep 20; loginctl list-sessions --no-legend | awk '{print $3}' | sort -u | tr '\n' ' '`, "")
	// The password opened /var at boot: GDM logs the person in, and there is no live user.
	r.add("logged in at boot", strings.Contains(sessions, lifecycleUser) && !strings.Contains(sessions, "live"), "sessions: "+sessions)
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

// installAsTheInstaller runs what spindrift-installer runs, from inside the live session (GNOME
// Shell starts it, so polkit sees the session's process), and answers what drift printed: its
// JSON records, then its errors.
func (g *guest) installAsTheInstaller() (string, error) {
	if _, err := g.waitShell(5 * time.Minute); err != nil {
		return "", err
	}
	run := "/run/user/1000/install"
	script := `printf '%s\n' '` + lifecyclePassword + `' | pkexec /usr/bin/drift install --yes --json --disk /dev/nvme0n1 ` +
		`--user ` + lifecycleUser + ` --real-name 'Ada Lovelace' --password-file - > ` + run + `.json 2> ` + run + `.err; echo $? > ` + run + `.rc`
	if _, err := g.eval(`GLib.spawn_async(null, ["sh", "-c", ` + strconv.Quote(script) + `], null, GLib.SpawnFlags.SEARCH_PATH, null); "started"`); err != nil {
		return "", err
	}
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		if rc, err := g.run(`cat `+run+`.rc 2>/dev/null`, ""); err == nil && strings.TrimSpace(rc) != "" {
			outs, _ := g.run(`cat `+run+`.json `+run+`.err`, "")
			if strings.TrimSpace(rc) != "0" {
				return outs, fmt.Errorf("drift install exited %s", strings.TrimSpace(rc))
			}
			return outs, nil
		}
		time.Sleep(5 * time.Second)
	}
	return "", fmt.Errorf("drift install did not end")
}
