package main

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// defaultHostname is the name a machine has until its person gives it one: os-release's
// DEFAULT_HOSTNAME (cmd/image), which systemd falls back to without /etc/hostname.
const defaultHostname = "construct"

// hostnameLabel is a name systemd and the network take as it is: one DNS label, which mDNS
// announces as <name>.local.
var hostnameLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validHostname is name as the machine's, lowercased: one label of letters, digits and hyphens,
// up to 63, neither end a hyphen.
func validHostname(name string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if !hostnameLabel.MatchString(n) {
		return "", fmt.Errorf("%q is no computer name: letters, digits and -, up to 63, not starting or ending with -", name)
	}
	return n, nil
}

// askHostname asks for the machine's name, the default taken on an empty line.
func askHostname(in *bufio.Reader, out io.Writer) (string, error) {
	for {
		fmt.Fprintf(out, "\n  Computer name [%s]: ", defaultHostname)
		line, err := readLine(in)
		if err != nil {
			return "", err
		}
		if line == "" {
			return defaultHostname, nil
		}
		name, err := validHostname(line)
		if err == nil {
			return name, nil
		}
		fmt.Fprintln(out, "  ", err)
	}
}
