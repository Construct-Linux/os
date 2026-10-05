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
	"sync/atomic"
	"time"
)

// lifecycle is a machine's life with Construct, in QEMU: installed from a stick onto its NVMe
// disk, booted from it, updated by plugging in the stick of the next release, and rolled back by
// systemd-boot from a release whose desktop never comes up - with what the person kept in /var and
// changed in /etc there all along. The sticks are three test builds, oldest first: task
// test:lifecycle makes them.
func lifecycle(args []string) error {
	fl := flag.NewFlagSet("lifecycle", flag.ExitOnError)
	dir := fl.String("dir", filepath.Join(out, "lifecycle"), "where v1.raw, v2.raw and v3.raw (broken) are, and the machine's disk and variables go")
	keys := fl.String("keys", filepath.Join(".cache", "test-keys"), "the test release key the sticks were signed with, which signs the channels' indexes too")
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
	// A stick is a 32 GB copy of a release's image, one per boot that plugs it in: removed once
	// that boot is over, or the copies fill the host's disk.
	stick := func(v string) (string, error) {
		s := filepath.Join(*dir, v+".stick")
		return s, stickCopy(filepath.Join(*dir, v+".raw"), s)
	}
	r := &report{dir: *dir}
	// The machine has a TPM, and every boot after the first of its disk is handed the person's
	// password as the TPM's PIN, as if typed: systemd-cryptsetup asks for it through libcryptsetup's
	// token plugin, which reads cryptsetup.luks2-pin. No passphrase credential: a TPM that refuses
	// leaves the boot waiting, and the check fails.
	m := machine{mem: vmMemory, pubkey: pub, vars: vars, nvme: nvme, tpm: tpm,
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
	// - pkexec, which the session's polkit rule lets through, construct install --json, the password
	// on standard input - read from the stick it runs from.
	outs, err := g.installAsTheInstaller()
	_ = os.WriteFile(filepath.Join(*dir, "install.txt"), []byte(outs), 0o644)
	if err != nil {
		// The live session's screen when the install did not end: a password asked, a dialog.
		_ = v.png(filepath.Join(*dir, "install.png"))
	}
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
	_ = os.Remove(s1)
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
	_ = v.png(filepath.Join(*dir, "password.png"))
	r.add("the disk asks for the password", asked, "screen: password.png")
	err = v.typeText(lifecyclePassword)
	// The outro goes back into the loop once the password is in, so the grid is there before GDM:
	// eight screens, one as soon as the one before is saved (~0.5 s each), since when it starts
	// depends on the TPM's unlock.
	for i := range 8 {
		_ = v.png(filepath.Join(*dir, fmt.Sprintf("password-after-%d.png", i)))
	}
	if err == nil {
		err = g.wait(5 * time.Minute)
	}
	r.add("the password opens the disk", err == nil, errText(err))
	if err != nil {
		v.stop()
		return r.write(nil)
	}
	r.add("boot the disk", g.version() == versions["v1"], "running "+g.version())
	r.plymouthPrompt(g, *dir)
	r.secureBoot(g)
	src, _ := g.run(`awk '$2=="/var"{print $1}' /proc/mounts; cryptsetup status var | grep -E 'type|device'`, "")
	r.add("/var on the disk, encrypted", strings.Contains(src, "/dev/mapper/var") && strings.Contains(src, "LUKS2") && strings.Contains(src, "nvme0n1p6"), oneLine(src))
	slots, _ := g.run(`systemd-cryptenroll /dev/nvme0n1p6`, "")
	r.add("the TPM, the password and the recovery key open it", strings.Contains(slots, "tpm2") && strings.Contains(slots, "password") && strings.Contains(slots, "recovery"), oneLine(slots))
	// The PIN typed is cached for the passphrase prompt that follows a TPM refusal, so a boot that
	// came up says nothing: the unit's log does.
	unlocked, err := g.run(`systemctl is-active systemd-cryptsetup@var.service; journalctl -b -o cat -u systemd-cryptsetup@var.service | grep -iE 'unseal|falling back|failed' | tail -3`, "")
	lang, _ := g.run(`cat /etc/locale.conf /etc/vconsole.conf; ls /boot/loader/credentials/; journalctl -b -o cat -u construct-boot-language.service | tail -2`, "")
	r.add("the person's language and keyboard, at boot too", strings.Contains(lang, "LANG=es_ES.UTF-8") && strings.Contains(lang, "XKBLAYOUT=es") &&
		strings.Contains(lang, "construct.vconsole.cred") && strings.Contains(lang, "construct.plymouth.cred") &&
		strings.Contains(lang, "Finished") && !strings.Contains(lang, "Failed"), oneLine(lang))
	r.add("the TPM unlocked it", err == nil && strings.HasPrefix(unlocked, "active") && strings.TrimSpace(strings.TrimPrefix(unlocked, "active")) == "", oneLine(unlocked))
	_, err = g.run(`echo kept > /var/home/`+lifecycleUser+`/marker && echo kept > /etc/construct-marker`, "")
	r.add("keep a file in /var and one in /etc", err == nil, errText(err))
	r.person(g)
	ok, detail := g.waitUnit2Active("boot-complete.target", 5*time.Minute)
	r.add("v1 comes up", ok, detail)
	v.powerOff(g)

	// 2b. A security key, enrolled beside the password with its PIN and a touch, opens the disk:
	// the boot asks for the key's PIN, not the password. Without the key the password still does,
	// and nothing about the key is an error.
	if !r.securityKey(m, first, *dir) {
		return r.write(nil)
	}

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
		_ = os.Remove(s)
	}

	// 4. v2 comes from its channel - the update CDN, played by an HTTP server on this host, which
	// the channels' transfers are pointed at - by the daily timer's service. stable lists v1 alone,
	// so the machine finds nothing newer there; following testing, it takes v2, each file through a
	// redirect as from the CDN to GitHub.
	cdn, err := newUpdateCDN(*dir, versions, filepath.Join(*keys, "release.pem"), filepath.Join(*keys, "release.pgp"))
	if err != nil {
		return err
	}
	defer cdn.srv.Close()
	url := cdn.url
	if v, g, err = boot(); err != nil {
		r.add("boot for the URL update", false, err.Error())
		return r.write(nil)
	}
	_, err = g.run(`for ch in stable testing; do d=/usr/lib/sysupdate.$ch.d; e=/etc/sysupdate.$ch.d; `+
		`[ $ch = stable ] && d=/usr/lib/sysupdate.d && e=/etc/sysupdate.d; mkdir -p $e && for f in $d/*.transfer; do `+
		`sed "s|^Path=https://.*|Path=`+url+`/$ch|" "$f" > $e/$(basename "$f") || exit 1; done; done`, "")
	r.add("point the channels at the test CDN", err == nil, url+errText(err))
	_, err = g.run(`systemctl start construct-update.service`, "")
	esp, _ := g.run(`ls /boot/EFI/Linux/`, "")
	r.add("stable has nothing newer", err == nil && !strings.Contains(esp, versions["v2"]), oneLine(esp)+errText(err))
	outs, err = g.run(`construct channel testing && construct channel`, "")
	r.add("follow testing", err == nil && strings.HasPrefix(outs, "following testing") && strings.Contains(outs, "\ntesting (of"), oneLine(outs)+errText(err))
	_, err = g.run(`systemctl start construct-update.service`, "")
	outs, _ = g.run(`journalctl -b --no-pager -o cat -u construct-update.service | tail -3`, "")
	r.add("v2 downloads from testing", err == nil, oneLine(outs)+errText(err))
	r.add("its files came through redirects", cdn.redirects.Load() >= 3, fmt.Sprintf("%d redirects", cdn.redirects.Load()))
	esp, _ = g.run(`ls /boot/EFI/Linux/`, "")
	r.add("v2 is counted on the ESP", strings.Contains(esp, "construct_"+versions["v2"]+"+3"), oneLine(esp))
	mark, _ := g.run(`cat /run/construct/reboot-required`, "")
	r.add("the person is told to reboot", strings.TrimSpace(mark) == versions["v2"], "reboot-required: "+oneLine(mark))
	// Told once: the mark stays until the reboot, and a path unit whose service went inactive
	// fires again while it does - a new notice each time, until the trigger limit fails the path.
	// The user manager's lines are matched by USER_UNIT: --user-unit, run as root, also asks for
	// root's UID, and found none.
	told, _ := g.run(fmt.Sprintf(`sleep 10; sudo -u %s XDG_RUNTIME_DIR=/run/user/1000 systemctl --user is-failed construct-reboot-notice.path; `+
		`journalctl -b -o cat USER_UNIT=construct-reboot-notice.service | grep -c '^Starting '`, lifecycleUser), "")
	once := strings.Join(strings.Fields(told), " ") == "active 1"
	r.add("the reboot notice is shown once", once, "path, notices: "+oneLine(told))
	if !once {
		// The path unit's and the notice's own account, and what the path is watched through.
		why, _ := g.run(fmt.Sprintf(`ls -ld /run/construct /run/construct/reboot-required; `+
			`sudo -u %s XDG_RUNTIME_DIR=/run/user/1000 systemctl --user status --no-pager construct-reboot-notice.path construct-reboot-notice.service; `+
			`journalctl -b --no-pager -o short-monotonic --user-unit construct-reboot-notice.path --user-unit construct-reboot-notice.service | tail -30`, lifecycleUser), "")
		_ = os.WriteFile(filepath.Join(*dir, "reboot-notice.txt"), []byte(why), 0o644)
	}
	g.reboot()
	if err := g.wait(5 * time.Minute); err != nil {
		// What the screen shows where the boot stopped: a prompt, an emergency shell.
		_ = v.png(filepath.Join(*dir, "reboot-v2.png"))
		r.add("reboot into v2", false, err.Error()+"; screen: reboot-v2.png")
		v.stop()
		return r.write(nil)
	}
	r.add("reboot into v2", g.version() == versions["v2"], "running "+g.version())
	kept, _ := g.run(`cat /var/home/`+lifecycleUser+`/marker /etc/construct-marker`, "")
	r.add("kept across the update", strings.Count(kept, "kept") == 2, oneLine(kept))
	ok, detail = g.waitUnit2Active("boot-complete.target", 5*time.Minute)
	r.add("v2 comes up", ok, detail)
	time.Sleep(10 * time.Second) // systemd-bless-boot runs once the target is reached
	esp, _ = g.run(`ls /boot/EFI/Linux/`, "")
	r.add("v2 is blessed", strings.Contains(esp, "construct_"+versions["v2"]+".efi"), oneLine(esp))
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
	if !u.ended {
		// Whether the stick's partition was seen and its unit started, and where it stopped.
		why, _ := g.run(`lsblk -o NAME,PARTLABEL,SIZE; systemctl list-units --all --no-pager 'construct-update*'; `+
			`for d in $(lsblk -nro NAME,PARTLABEL | awk '$2 == "construct-update" {print $1}'); do udevadm info /dev/$d | grep -E 'SYSTEMD|PARTNAME|TAGS'; done; `+
			`journalctl -b --no-pager -o short-monotonic -u 'construct-update@*' | tail -40`, "")
		_ = os.WriteFile(filepath.Join(*dir, "stick-update.txt"), []byte(why), 0o644)
	}
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
	r.add("v3 is marked bad", strings.Contains(esp, "construct_"+versions["v3"]+"+0-3"), oneLine(esp))
	kept, _ = g.run(`cat /var/home/`+lifecycleUser+`/marker /etc/construct-marker`, "")
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
	gone, _ := g.run(`ls /var/home/; getent passwd `+lifecycleUser+`; cat /etc/construct-marker 2>&1`, "")
	r.add("factory reset forgets the person", !strings.Contains(gone, lifecycleUser) && !strings.Contains(gone, "kept"), oneLine(gone))
	setup, _ := g.run(`for i in $(seq 60); do loginctl list-sessions --no-legend | grep -q gnome-initial-setup && break; sleep 2; done; loginctl list-sessions --no-legend | awk '{print $3}' | sort -u | tr '\n' ' '`, "")
	r.add("initial setup for the new person", strings.Contains(setup, "gnome-initial-setup"), "sessions: "+setup)
	err = r.write(g)
	v.powerOff(g)
	_ = os.Remove(s3)
	return err
}

// lifecycleKeyPIN is the security key's PIN: another secret than the person's password, as a
// YubiKey's is.
const lifecycleKeyPIN = "482913"

// securityKey runs step 2b on the installed machine m (first is it without the password's
// credential, as a person boots it): a new key plugged in, added from the running system by
// construct key with the person's password and the PIN it gives the key - opening the disk and
// signing them in - then the boots with and without it. It reports whether the machine still boots for the steps after it. A QEMU without CanoKey
// has no key to plug in, and says so.
func (r *report) securityKey(m, first machine, dir string) bool {
	if !qemuHasCanoKey() {
		r.skip("a security key opens the disk", "this host's QEMU has no CanoKey (built without libcanokey-qemu)")
		return true
	}
	key := filepath.Join(dir, "canokey")
	_ = os.Remove(key)
	withKey := m
	withKey.securityKey = key
	v, g, err := withKey.bootGuest(5 * time.Minute)
	if err != nil {
		r.add("boot with a security key not yet enrolled", false, err.Error())
		return false
	}
	// A new key, no PIN yet, added as the app adds it: construct key, the password and then the PIN
	// on its standard input; construct gives the key the PIN before it enrolls it.
	enroll, err := g.run(`dev=$(fido2-token -L | grep -m1 -i canokey | cut -d: -f1) && `+
		`printf '%s\n%s\n' '`+lifecyclePassword+`' '`+lifecycleKeyPIN+`' | timeout 120 construct key add --device $dev --name YubiKey --user `+lifecycleUser+` --pin-file - 2>&1; `+
		`rc=$?; fido2-token -I $dev | grep options; systemd-cryptenroll /dev/nvme0n1p6; construct key list; exit $rc`, "")
	r.add("enroll a security key", err == nil && strings.Contains(enroll, "fido2") && strings.Contains(enroll, "YubiKey\t"+lifecycleUser+"\topens the disk") &&
		strings.Contains(enroll, " clientPin"),
		oneLine(enroll)+errText(err))
	// The key signs the person in where sudo asks: a wrong password, and the touch lets them in.
	sudo, err := g.run(`runuser -u `+lifecycleUser+` -- sh -c 'echo wrong | sudo -S -k true' 2>&1; echo rc=$?`, "")
	r.add("the key's touch is sudo's password", err == nil && strings.HasSuffix(strings.TrimSpace(sudo), "rc=0"), oneLine(sudo)+errText(err))
	v.powerOff(g)

	keyBoot := first
	keyBoot.securityKey = key
	if v, err = keyBoot.start(); err != nil {
		r.add("boot with the security key", false, err.Error())
		return false
	}
	g = &guest{cid: v.cid}
	asked := g.wait(45*time.Second) != nil
	_ = v.png(filepath.Join(dir, "key-pin.png"))
	r.add("the disk asks for the key's PIN", asked, "screen: key-pin.png")
	// The key's PIN is no password: had the boot asked for the password, this would not open it.
	err = v.typeText(lifecycleKeyPIN)
	if err == nil {
		err = g.wait(5 * time.Minute)
	}
	r.add("the key's PIN and a touch open the disk", err == nil, errText(err))
	if err != nil {
		v.stop()
		return false
	}
	used, _ := g.run(`journalctl -b -q -o cat -u systemd-cryptsetup@var.service -p err; echo end`, "")
	if strings.TrimSpace(used) != "end" {
		// The whole unit's words: which attempt, which token, and what opened /var in the end.
		used, _ = g.run(`journalctl -b -q -o short-monotonic -u systemd-cryptsetup@var.service`, "")
	}
	r.add("no error opening it with the key", strings.TrimSpace(used) == "end", oneLine(used))
	// No password was typed, and the keyring is open all the same.
	keyring, _ := g.run(`sleep 20; `+keyringRoundTrip(lifecycleUser, 1000), "")
	r.add("the login keyring is open after the key", strings.TrimSpace(keyring) == "kept", oneLine(keyring))
	v.powerOff(g)

	v, g, err = m.bootGuest(5 * time.Minute)
	if err != nil {
		r.add("the password opens it without the key", false, err.Error())
		return false
	}
	without, _ := g.run(`systemctl is-active systemd-cryptsetup@var.service; journalctl -b -q -o cat -u systemd-cryptsetup@var.service -p err; echo end`, "")
	r.add("the password opens it without the key", strings.TrimSpace(without) == "active\nend", oneLine(without))
	// Without the key pam_u2f steps aside: sudo takes the password, and says nothing of the key.
	sudo, _ = g.run(`runuser -u `+lifecycleUser+` -- sh -c "echo '`+lifecycleNewPassword+`' | sudo -S -k true" 2>&1; echo rc=$?; journalctl -b -q -o cat -p warning -t sudo; echo end`, "")
	r.add("sudo takes the password without the key", strings.HasSuffix(strings.TrimSpace(sudo), "rc=0\nend") && !strings.Contains(sudo, "pam_u2f"), oneLine(sudo))
	v.powerOff(g)
	return true
}

// updateEnd is how the construct-update@ unit a plugged stick started ended: whether it did, and
// well, systemd's line for it, and what systemd-sysupdate last said.
type updateEnd struct {
	ended, ok       bool
	unit, sysupdate string
}

func (u updateEnd) detail() string {
	if !u.ended {
		return "no construct-update@ unit ended"
	}
	return u.unit + ": systemd-sysupdate: " + oneLine(u.sysupdate)
}

// waitUpdate waits for the construct-update@ unit a plugged stick started to end. It reads the
// journal: a oneshot that already ended is unloaded, and no longer listed.
func (g *guest) waitUpdate(d time.Duration) updateEnd {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		log, _ := g.run(`journalctl -b --no-pager -o cat -u 'construct-update@*' | grep -E '^(Finished|Failed to start) Update' | tail -1`, "")
		if log = strings.TrimSpace(log); log != "" {
			tail, _ := g.run(`journalctl -b --no-pager -o cat -u 'construct-update@*' -t systemd-sysupdate | tail -3`, "")
			return updateEnd{ended: true, ok: strings.HasPrefix(log, "Finished"), unit: log, sysupdate: strings.TrimSpace(tail)}
		}
		time.Sleep(3 * time.Second)
	}
	return updateEnd{}
}

// updateCDN is the update CDN (infra updates.tf) on this host, where a guest reaches it at
// 10.0.2.2 (QEMU's user network): each channel's index, signed with the test release key, and each
// release file as a redirect to where it is - a path of this server here, a GitHub Release asset
// there.
type updateCDN struct {
	srv       *http.Server
	url       string
	redirects atomic.Int32
}

// newUpdateCDN serves stable listing v1 and testing listing v1 and v2, from the releases in dir.
func newUpdateCDN(dir string, versions map[string]string, keyRef, pubring string) (*updateCDN, error) {
	root := filepath.Join(dir, "cdn")
	if err := os.RemoveAll(root); err != nil {
		return nil, err
	}
	byVersion := map[string]string{}
	for ch, vs := range map[string][]string{"stable": {"v1"}, "testing": {"v1", "v2"}} {
		idx := index{}
		for _, v := range vs {
			b, err := os.ReadFile(filepath.Join(dir, v+".release", "SHA256SUMS"))
			if err != nil {
				return nil, err
			}
			rel, err := parseIndex(string(b), releaseFile)
			if err != nil {
				return nil, err
			}
			idx[versions[v]] = rel[versions[v]]
			byVersion[versions[v]] = v
		}
		sums := filepath.Join(root, ch, "SHA256SUMS")
		if err := os.MkdirAll(filepath.Dir(sums), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(sums, []byte(idx.String()), 0o644); err != nil {
			return nil, err
		}
		if err := signSums(keyRef, pubring, sums); err != nil {
			return nil, err
		}
	}
	c := &updateCDN{}
	index := http.FileServer(http.Dir(root))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) == 2 {
			if m := releaseFile.FindStringSubmatch(parts[1]); m != nil {
				if v, ok := byVersion[m[1]]; ok && parts[0] != "assets" {
					c.redirects.Add(1)
					http.Redirect(w, r, "/assets/"+v+"/"+parts[1], http.StatusFound)
					return
				}
			}
		}
		if len(parts) == 3 && parts[0] == "assets" && releaseFile.MatchString(parts[2]) && (parts[1] == "v1" || parts[1] == "v2") {
			http.ServeFile(w, r, filepath.Join(dir, parts[1]+".release", parts[2]))
			return
		}
		index.ServeHTTP(w, r)
	})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	c.srv = &http.Server{Handler: handler}
	go func() { _ = c.srv.Serve(l) }()
	c.url = fmt.Sprintf("http://10.0.2.2:%d", l.Addr().(*net.TCPAddr).Port)
	return c, nil
}

// The person test:lifecycle installs for.
const (
	lifecycleUser     = "ada"
	lifecyclePassword = "correct horse battery staple"
	// lifecycleNewPassword is the one the person changes theirs to (person), before the security
	// key's step: the disk still opens with the first, which LUKS keeps, and sudo takes this one.
	lifecycleNewPassword = "quiet lantern harbor seven"
	// lifecycleHostname is the name the person gives the machine, which it has from its first boot.
	lifecycleHostname = "ada-x9"
)

// person checks who an installed machine is for: logged in once their password opened /var, no live
// user, and sudo asks for their password and takes it.
func (r *report) person(g *guest) {
	sessions, _ := g.run(`sleep 20; loginctl list-sessions --no-legend | awk '{print $3}' | sort -u | tr '\n' ' '`, "")
	// The password opened /var at boot: GDM logs the person in, and there is no live user.
	r.add("logged in at boot", strings.Contains(sessions, lifecycleUser) && !strings.Contains(sessions, "live"), "sessions: "+sessions)
	name, _ := g.run(`hostnamectl hostname`, "")
	r.add("the computer's name", strings.TrimSpace(name) == lifecycleHostname, "hostname: "+oneLine(name))
	// Their login keyring has no password of its own (construct clearKeyring): open once GDM logs
	// them in, an app keeps and reads a secret with no prompt.
	keyring, _ := g.run(keyringRoundTrip(lifecycleUser, 1000), "")
	r.add("the login keyring is open", strings.TrimSpace(keyring) == "kept", oneLine(keyring))
	noPw, _ := g.run(`sudo -u `+lifecycleUser+` sudo -n true 2>&1; echo rc=$?`, "")
	r.add("sudo asks for the password", strings.Contains(noPw, "rc=1"), oneLine(noPw))
	withPw, _ := g.run(`printf '%s\n' '`+lifecyclePassword+`' | sudo -u `+lifecycleUser+` sudo -S -p '' id -u 2>&1; echo rc=$?`, "")
	r.add("sudo takes the password", strings.Contains(withPw, "0\nrc=0") || strings.HasPrefix(strings.TrimSpace(withPw), "0"), oneLine(withPw))
	// The person changes their own password, where Settings and passwd change it (pam_unix,
	// /etc/shadow), and sudo then takes the new one.
	changed, err := g.run(`printf '%s\n%s\n%s\n' '`+lifecyclePassword+`' '`+lifecycleNewPassword+`' '`+lifecycleNewPassword+`' | runuser -u `+lifecycleUser+` -- passwd 2>&1; echo rc=$?`, "")
	r.add("the person changes their password", err == nil && strings.Contains(changed, "rc=0"), oneLine(changed))
	newPw, _ := g.run(`printf '%s\n' '`+lifecycleNewPassword+`' | sudo -u `+lifecycleUser+` sudo -k -S -p '' id -u 2>&1; echo rc=$?`, "")
	r.add("sudo takes the new password", strings.HasPrefix(strings.TrimSpace(newPw), "0"), oneLine(newPw))
	root, _ := g.run(`getent shadow root | cut -d: -f2`, "")
	r.add("root has no password", strings.TrimSpace(root) == "*", "root: "+strings.TrimSpace(root))
}

// sessionsScript prints each login session's user, class and whether it is the seat's active one.
const sessionsScript = `for s in $(loginctl list-sessions --no-legend | awk '{print $1}'); do ` +
	`echo "session $s $(loginctl show-session $s -p Name -p Class -p Active --value | tr '\n' ' ')"; done; `

// installAsTheInstaller runs what construct-installer runs, from inside the live session (GNOME
// Shell starts it, so polkit sees the session's process), and answers what construct printed: its
// JSON records, then its errors.
func (g *guest) installAsTheInstaller() (string, error) {
	if _, err := g.waitShell(5 * time.Minute); err != nil {
		// Which session the seat shows - a greeter in front of the live one, once - and what GDM said.
		outs, _ := g.run(sessionsScript+`journalctl -b -q -o short-monotonic --no-pager -u gdm -u construct-live -t gdm-autologin -t gnome-shell -t systemd-logind | tail -60`, "")
		return outs, err
	}
	run := "/run/user/1000/install"
	script := `printf '%s\n' '` + lifecyclePassword + `' | pkexec /usr/bin/construct install --yes --json --disk /dev/nvme0n1 ` +
		`--user ` + lifecycleUser + ` --real-name 'Ada Lovelace' --password-file - ` +
		// The session's language and layout, as the installer passes them: Spanish, so the
		// machine's boot screen and keyboard are checked to be the person's, not the image's.
		`--locale es_ES.UTF-8 --keyboard es --hostname ` + lifecycleHostname + ` > ` + run + `.json 2> ` + run + `.err; echo $? > ` + run + `.rc`
	if _, err := g.eval(`GLib.spawn_async(null, ["sh", "-c", ` + strconv.Quote(script) + `], null, GLib.SpawnFlags.SEARCH_PATH, null); "started"`); err != nil {
		return "", err
	}
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		if rc, err := g.run(`cat `+run+`.rc 2>/dev/null`, ""); err == nil && strings.TrimSpace(rc) != "" {
			outs, _ := g.run(`cat `+run+`.json `+run+`.err`, "")
			if strings.TrimSpace(rc) != "0" {
				return outs, fmt.Errorf("construct install exited %s", strings.TrimSpace(rc))
			}
			return outs, nil
		}
		time.Sleep(5 * time.Second)
	}
	// What it was doing when the time ran out: its progress so far, its errors, a password the
	// session was asked for, and the journal's last words.
	outs, _ := g.run(`tail -3 `+run+`.json; tail -5 `+run+`.err; ps -eo pid,etime,args | grep -E 'pkexec|construct install|polkit' | grep -v grep; `+
		sessionsScript+`journalctl -b -q -o short-monotonic -n 25 --no-pager`, "")
	return outs, fmt.Errorf("construct install did not end")
}
