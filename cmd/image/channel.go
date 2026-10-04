package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// A channel is what machines that follow it may take: a SHA256SUMS that lists the files of a few
// releases, signed with the release key, at <updates>/<channel>/ (infra updates.tf). systemd-
// sysupdate reads it as a release's own SHA256SUMS and takes the newest version it lists; a
// version taken out of it is withdrawn (CoreOS's dead end), and promoting a release is adding its
// lines to the next channel's index. The files themselves are GitHub Release assets the CDN
// redirects to, so an index is the only thing a channel holds.

// channels are the channels machines follow: the default sysupdate component is the first, each
// other its own component (sysupdate.<channel>.d, construct channel).
var channels = []string{"stable", "testing"}

// releaseFile is a release file's name: its version is what a channel keeps or drops it by.
var releaseFile = regexp.MustCompile(`^construct_([0-9]{8}\.[0-9]+)(?:_[0-9a-f-]{36}\.(?:root|verity)\.raw\.xz|\.efi)$`)

// index is a channel's SHA256SUMS: the lines of each version it lists.
type index map[string][]string

// parseIndex reads a SHA256SUMS: "<sha256>  <file>" lines, by the file's version. A line naming
// anything but a release file is refused: an index lists releases and nothing else.
func parseIndex(sums string) (index, error) {
	idx := index{}
	for _, line := range strings.Split(sums, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || len(f[0]) != 64 {
			return nil, fmt.Errorf("index: %q is not a SHA256SUMS line", line)
		}
		m := releaseFile.FindStringSubmatch(strings.TrimPrefix(f[1], "*"))
		if m == nil {
			return nil, fmt.Errorf("index: %q is not a release's file", f[1])
		}
		idx[m[1]] = append(idx[m[1]], f[0]+"  "+strings.TrimPrefix(f[1], "*"))
	}
	return idx, nil
}

// versions are the index's versions, newest first, compared as systemd-sysupdate does: segment
// by segment, numerically.
func (idx index) versions() []string {
	vs := make([]string, 0, len(idx))
	for v := range idx {
		vs = append(vs, v)
	}
	sort.Slice(vs, func(i, j int) bool { return newer(vs[i], vs[j]) })
	return vs
}

// newer is whether version a is newer than b.
func newer(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x > y
		}
	}
	return len(as) > len(bs)
}

// String is the index as SHA256SUMS, newest version first.
func (idx index) String() string {
	var b strings.Builder
	for _, v := range idx.versions() {
		lines := append([]string(nil), idx[v]...)
		sort.Strings(lines)
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

// keep drops all but the newest n versions: what a machine might still need, a release to take
// and the one before it to go back to, and no more for systemd-sysupdate to consider.
func (idx index) keep(n int) {
	for i, v := range idx.versions() {
		if i >= n {
			delete(idx, v)
		}
	}
}

// channelIndex writes a channel's index: the current one (-current, empty for a new channel) with
// a release added (-add, a release directory or another channel's index with -version) or a
// version withdrawn (-withdraw), and the newest -keep versions kept. task release:publish and
// release:promote sign it (image sign-release) and upload it.
func channelIndex(args []string) error {
	fl := flag.NewFlagSet("channel-index", flag.ExitOnError)
	current := fl.String("current", "", "the channel's index now (empty: a new channel)")
	add := fl.String("add", "", "a SHA256SUMS whose -version lines to add: a release's own, or another channel's index")
	version := fl.String("version", "", "the version -add brings, or -withdraw takes out")
	withdraw := fl.Bool("withdraw", false, "take -version out instead of adding it")
	keep := fl.Int("keep", 3, "how many versions the index keeps")
	to := fl.String("o", "", "where the new index goes (a directory: its SHA256SUMS)")
	_ = fl.Parse(args)
	if *version == "" || *to == "" || (*add == "") == !*withdraw {
		return errors.New("channel-index: -version and -o, and -add or -withdraw")
	}
	idx := index{}
	if *current != "" {
		b, err := os.ReadFile(*current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if idx, err = parseIndex(string(b)); err != nil {
			return fmt.Errorf("%s: %w", *current, err)
		}
	}
	if *withdraw {
		if _, ok := idx[*version]; !ok {
			return fmt.Errorf("the channel does not list %s", *version)
		}
		delete(idx, *version)
	} else {
		b, err := os.ReadFile(*add)
		if err != nil {
			return err
		}
		from, err := parseIndex(string(b))
		if err != nil {
			return fmt.Errorf("%s: %w", *add, err)
		}
		lines, ok := from[*version]
		if !ok || len(lines) != 3 {
			return fmt.Errorf("%s lists %d files of %s, want its 3 (root, verity, kernel image)", *add, len(lines), *version)
		}
		idx[*version] = lines
		idx.keep(*keep)
	}
	if err := os.MkdirAll(*to, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*to, "SHA256SUMS"), []byte(idx.String()), 0o644)
}
