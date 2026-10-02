package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree lays files (name -> content; content "->target" makes a symlink) under a new root.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if target, ok := strings.CutPrefix(body, "->"); ok {
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// A root whose configuration names only what it has passes.
func TestRefsOfAWholeRoot(t *testing.T) {
	root := tree(t, map[string]string{
		"etc/passwd":                        "root:x:0:0::/root:/bin/sh\nbluetooth:x:9:9::/:/bin/false\n",
		"etc/group":                         "root:x:0:\nwheel:x:10:\n",
		"usr/bin/bluetoothd":                "",
		"etc/shells":                        "/bin/sh\n/bin/bash\n",
		"bin/sh":                            "",
		"bin/bash":                          "",
		"usr/lib/systemd/system/bt.service": "[Service]\nExecStart=-/usr/bin/bluetoothd -n\nUser=bluetooth\n[Install]\nAlias=dbus-org.bluez.service\n",
		"etc/systemd/system/dbus-org.bluez.service":            "->/usr/lib/systemd/system/bt.service",
		"etc/systemd/system/bluetooth.target.wants/bt.service": "->/usr/lib/systemd/system/bt.service",
		"etc/systemd/system/masked.service":                    "->/dev/null",
		"usr/share/dbus-1/system-services/org.bluez.service":   "[D-BUS Service]\nName=org.bluez\nExec=/bin/false\nUser=root\nSystemdService=dbus-org.bluez.service\n",
		"etc/pam.d/base-auth":                                  "auth required pam_unix.so\n",
		"etc/pam.d/login":                                      "auth include base-auth\n-auth optional pam_gnome_keyring.so\n",
		"usr/lib/security/pam_unix.so":                         "",
		"usr/lib/tmpfiles.d/x.conf":                            "d /var/lib/x 0755 root wheel -\nd /run/y 0700 %u - -\n",
		"usr/lib/udev/rules.d/50.rules":                        `KERNEL=="tpm[0-9]*", MODE="0660", GROUP="wheel"` + "\n",
	})
	if got := refs(root); len(got) != 0 {
		t.Fatalf("a whole root has problems: %q", got)
	}
}

// Each kind of broken reference the image has shipped is caught.
func TestRefsFindWhatIsMissing(t *testing.T) {
	root := tree(t, map[string]string{
		"etc/passwd": "root:x:0:0::/root:/bin/sh\n",
		"etc/group":  "root:x:0:\n",
		// Wolfi's base layout's /etc/shells: BusyBox's alone.
		"etc/shells": "/bin/sh\n/bin/ash\n",
		"bin/sh":     "",
		// bluez without its unit: the enable link points at nothing.
		"etc/systemd/system/bluetooth.target.wants/bluetooth.service": "->/usr/lib/systemd/system/bluetooth.service",
		// an enabled unit whose program and user are not there.
		"usr/lib/systemd/system/geo.service":                     "[Service]\nExecStart=/usr/libexec/geoclue\nUser=geoclue\n",
		"etc/systemd/system/multi-user.target.wants/geo.service": "->/usr/lib/systemd/system/geo.service",
		// a bus service whose systemd unit does not exist.
		"usr/share/dbus-1/system-services/org.freedesktop.timesync1.service": "[D-BUS Service]\nExec=/bin/false\nSystemdService=dbus-org.freedesktop.timesync1.service\n",
		// Fedora's PAM dialect, and a required module that is not installed.
		"etc/pam.d/polkit-1": "auth include system-auth\nsession required pam_keyinit.so\n",
		// a udev rule for a group nobody declares.
		"usr/lib/udev/rules.d/60-tpm.rules": `KERNEL=="tpmrm[0-9]*", OWNER="tss", GROUP="tss"` + "\n",
	})
	got := strings.Join(refs(root), "\n")
	for _, want := range []string{
		"bluetooth.service links to /usr/lib/systemd/system/bluetooth.service",
		"geo.service: ExecStart=/usr/libexec/geoclue: no such program",
		"geo.service: User=geoclue is declared nowhere",
		"SystemdService=dbus-org.freedesktop.timesync1.service: no unit is or declares it",
		"polkit-1: includes system-auth",
		"polkit-1: needs pam_keyinit.so",
		"60-tpm.rules: owner tss",
		"60-tpm.rules: group tss",
		"etc/shells does not name /bin/bash",
		"/bin/bash, a user's shell, is not in the root",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("not found: %q\nin:\n%s", want, got)
		}
	}
}
