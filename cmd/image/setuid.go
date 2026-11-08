package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// privileged is every program the root runs setuid or setgid, and why it needs to; capable, every
// one with file capabilities. Both run with more rights than their caller's. Elevation is otherwise asked of a
// privileged process over IPC (polkit, systemd's run0): a setuid program runs its caller's
// environment, arguments and file descriptors with root's rights, and each is an attack surface
// a person on a one-person desktop rarely uses.
var privileged = map[string]string{
	"usr/bin/sudo": "where a person becomes root (etc/pam.d/sudo: their password, or the security key's touch)",
	"usr/bin/passwd": "a person changes their own password in a terminal (test:lifecycle's person); " +
		"Settings changes it through accountsservice, as root",
	"usr/bin/pkexec": "the installer and construct-1password ask polkit through it for construct install " +
		"and construct 1password install",
	"usr/bin/fusermount3": "FUSE mounts a person's processes make: the document portal Flatpaks open files through, " +
		"gvfsd-fuse, AppImages",
}

var capable = map[string]string{
	"usr/bin/newuidmap": "rootless containers (Docker rootless, Podman) map a person's subordinate IDs",
	"usr/bin/newgidmap": "as newuidmap, for groups",
}

// unprivileged is what the packages make setuid or setgid and the image does not need to be, and
// why: bake clears the bits. Each still runs, with its caller's rights.
var unprivileged = map[string]string{
	"usr/bin/chage":   "password ageing, which a one-person machine does not set",
	"usr/bin/chfn":    "the full name: Settings changes it through accountsservice, as root",
	"usr/bin/chsh":    "the login shell: one person, whose shell is bash (internal/account, etc/shells)",
	"usr/bin/gpasswd": "group passwords, which nothing sets",
	"usr/bin/newgrp":  "a group's password, which nothing sets: the person's groups are theirs at login",
	"usr/bin/wall":    "writing to other users' terminals: there is one person",
	"usr/bin/su":      "root is locked, and runuser is root's way to another account",
	"usr/bin/mount":   "no fstab entry lets a user mount; udisks mounts for the desktop, construct install as root",
	"usr/bin/umount":  "as mount",
	"usr/lib/polkit-1/polkit-agent-helper-1": "polkit-agent-helper.socket runs it as a service instead " +
		"(usr/lib/systemd/system/polkit-agent-helper@.service.d)",
}

// limitPrivilege clears the setuid and setgid bits of unprivileged's programs in root and refuses
// a root whose programs with more rights are not exactly privileged's and capable's: a package that adds one, or
// drops one, is a decision this file records, not one the build takes alone.
func limitPrivilege(root string) error {
	found, err := privilegedFiles(root)
	if err != nil {
		return err
	}
	var problems []string
	for path, mode := range found {
		if _, ok := unprivileged[path]; ok {
			if err := os.Chmod(filepath.Join(root, path), mode.Perm()); err != nil {
				return err
			}
			continue
		}
		_, p := privileged[path]
		_, c := capable[path]
		if !p && !c {
			problems = append(problems, path+" is setuid, setgid or has capabilities: add it to privileged or capable with its reason, or to unprivileged")
		}
	}
	for _, list := range []map[string]string{privileged, capable} {
		for path := range list {
			if _, ok := found[path]; !ok {
				problems = append(problems, path+" is listed as privileged but the root does not make it so: take it out")
			}
		}
	}
	for path := range unprivileged {
		if _, ok := found[path]; !ok {
			problems = append(problems, path+" is in unprivileged but the root does not make it so: take it out")
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New("privileged programs (cmd/image/setuid.go):\n  " + strings.Join(problems, "\n  "))
	}
	return nil
}

// privilegedFiles is every regular file under root, by its path relative to root, that is setuid,
// setgid or carries file capabilities, with its mode.
func privilegedFiles(root string) (map[string]fs.FileMode, error) {
	found := map[string]fs.FileMode{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		caps, err := hasCapabilities(path)
		if err != nil {
			return err
		}
		if info.Mode()&(fs.ModeSetuid|fs.ModeSetgid) == 0 && !caps {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		found[filepath.ToSlash(rel)] = info.Mode()
		return nil
	})
	return found, err
}

func hasCapabilities(path string) (bool, error) {
	_, err := unix.Lgetxattr(path, "security.capability", nil)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.ENODATA), errors.Is(err, unix.ENOTSUP):
		return false, nil
	}
	return false, fmt.Errorf("%s: %w", path, err)
}

// setuidOnRoot is the paths a booted root's find lists as setuid or setgid: privileged's, sorted
// and space separated.
func setuidOnRoot() string {
	var paths []string
	for path := range privileged {
		paths = append(paths, "/"+path)
	}
	sort.Strings(paths)
	return strings.Join(paths, " ")
}
