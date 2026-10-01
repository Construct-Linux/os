package update

import (
	"bufio"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ukiOnESP is a kernel image's name on the ESP as systemd-sysupdate and systemd-boot write it:
// <ImageID>_<version>.efi when blessed, or with +<tries left>[-<tries done>] while it is counted.
var ukiOnESP = regexp.MustCompile(`^` + ImageID + `_` + versionPattern + `(?:\+([0-9]+)(?:-([0-9]+))?)?\.efi$`)

// Pending is the version systemd-boot starts at the next boot instead of booted, from the names
// of the kernel images on the ESP (EFI/Linux): the newest one newer than booted that it will still
// try - blessed, or counted with tries left. A version whose tries ran out is the one
// it fell back from, and is not waiting for anything. It answers "" when nothing is.
func Pending(names []string, booted string) string {
	pending := ""
	for _, name := range names {
		m := ukiOnESP.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		if m[2] != "" {
			if left, _ := strconv.Atoi(m[2]); left == 0 {
				continue
			}
		}
		if Compare(m[1], booted) > 0 && (pending == "" || Compare(m[1], pending) > 0) {
			pending = m[1]
		}
	}
	return pending
}

// BootedVersion is the IMAGE_VERSION in the os-release at path: the version the machine runs.
func BootedVersion(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "IMAGE_VERSION="); ok {
			return strings.Trim(v, `"'`), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", os.ErrNotExist
}
