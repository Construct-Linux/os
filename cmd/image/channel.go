package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Construct-Linux/os/internal/update"
)

// A channel is what machines that follow it may take: a SHA256SUMS that lists the files of a few
// releases, signed with the release key, at <updates>/<channel>/ (infra updates.tf). systemd-
// sysupdate reads it as a release's own SHA256SUMS and takes the newest version it lists; a
// version taken out of it is withdrawn (CoreOS's dead end), and promoting a release is adding its
// lines to the next channel's index. The files themselves are each release's, under
// <updates>/releases/<version>/, which the CDN redirects a channel's file to, so an index is the
// only thing a channel holds.

// channels are the channels machines follow: the default sysupdate component is the first, each
// other its own component (sysupdate.<channel>.d, construct channel).
var channels = []string{"stable", "testing"}

// releaseFile is a release file's name: its version is what a channel keeps or drops it by.
var releaseFile = regexp.MustCompile(`^construct_([0-9]{8}\.[0-9]+)(?:_[0-9a-f-]{36}\.(?:root|verity)\.raw\.xz|\.efi)$`)

// stickFile is a stick's name at dl.<domain>, one file a version, which dl's SHA256SUMS lists as
// a channel's index lists releases.
var stickFile = regexp.MustCompile(`^construct_([0-9]{8}\.[0-9]+)\.raw\.xz$`)

// indexKinds are the indexes channel-index writes: the files each lists, and how many a version
// has.
var indexKinds = map[string]struct {
	file  *regexp.Regexp
	files int
}{
	"channel": {releaseFile, 3}, // root, verity, kernel image
	"dl":      {stickFile, 1},
}

// index is a channel's SHA256SUMS: the lines of each version it lists.
type index map[string][]string

// parseIndex reads a SHA256SUMS: "<sha256>  <file>" lines, by the file's version. A line naming
// anything but a file the index's kind lists is refused: an index lists releases and nothing else.
func parseIndex(sums string, file *regexp.Regexp) (index, error) {
	idx := index{}
	for _, line := range strings.Split(sums, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || len(f[0]) != 64 {
			return nil, fmt.Errorf("index: %q is not a SHA256SUMS line", line)
		}
		m := file.FindStringSubmatch(strings.TrimPrefix(f[1], "*"))
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
	sort.Slice(vs, func(i, j int) bool { return update.Compare(vs[i], vs[j]) > 0 })
	return vs
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

// indexVersions is how many versions an index keeps (index.keep).
const indexVersions = 3

// keep drops all but the newest n versions: what a machine might still need, a release to take
// and the one before it to go back to, and no more for systemd-sysupdate to consider. It returns
// the files of the versions it dropped.
func (idx index) keep(n int) (dropped []string) {
	for i, v := range idx.versions() {
		if i >= n {
			for _, l := range idx[v] {
				dropped = append(dropped, strings.Fields(l)[1])
			}
			delete(idx, v)
		}
	}
	sort.Strings(dropped)
	return dropped
}

// channelIndex writes a channel's index: the current one (-current, or -new for a channel's first)
// with a release added (-add, a release's SHA256SUMS or another channel's index, with -version) or a
// version withdrawn (-withdraw), and the newest indexVersions kept. task release:publish and
// release:promote sign it (image sign-release) and upload it. -kind dl is the same for the sticks
// at dl.<domain>: its "dropped" lists the sticks the index no longer names, which the task deletes
// only after the new index is up.
func channelIndex(args []string) error {
	fl := flag.NewFlagSet("channel-index", flag.ExitOnError)
	kindName := fl.String("kind", "channel", "the index: a channel's (channel), or the sticks' at dl.<domain> (dl)")
	current := fl.String("current", "", "the channel's index now")
	first := fl.Bool("new", false, "the channel has no index yet: this is its first")
	add := fl.String("add", "", "a SHA256SUMS whose -version lines to add: a release's own, or another channel's index")
	version := fl.String("version", "", "the version -add brings, or -withdraw takes out")
	withdraw := fl.Bool("withdraw", false, "take -version out instead of adding it")
	to := fl.String("o", "", "where the new index goes (a directory: its SHA256SUMS)")
	_ = fl.Parse(args)
	if *version == "" || *to == "" || (*add == "") == !*withdraw {
		return errors.New("channel-index: -version and -o, and -add or -withdraw")
	}
	// A channel is new only when someone says so: an index that could not be fetched, taken as
	// empty, would be replaced by one listing a single release, and the versions machines fall
	// back to would be gone.
	if (*current == "") == !*first {
		return errors.New("channel-index: -current, or -new for a channel's first index")
	}
	kind, ok := indexKinds[*kindName]
	if !ok {
		return fmt.Errorf("channel-index: -kind %q is neither channel nor dl", *kindName)
	}
	idx := index{}
	if *current != "" {
		b, err := os.ReadFile(*current)
		if err != nil {
			return err
		}
		if idx, err = parseIndex(string(b), kind.file); err != nil {
			return fmt.Errorf("%s: %w", *current, err)
		}
	}
	var dropped []string
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
		from, err := parseIndex(string(b), kind.file)
		if err != nil {
			return fmt.Errorf("%s: %w", *add, err)
		}
		lines, ok := from[*version]
		if !ok || len(lines) != kind.files {
			return fmt.Errorf("%s lists %d files of %s, want its %d", *add, len(lines), *version, kind.files)
		}
		idx[*version] = lines
		dropped = idx.keep(indexVersions)
		// Older than every version kept, it was dropped at once: the index is the one already up,
		// and signing and uploading it again would say the release was offered.
		if _, ok := idx[*version]; !ok {
			return fmt.Errorf("%s is older than the %d versions the index keeps", *version, indexVersions)
		}
	}
	if err := os.MkdirAll(*to, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*to, "SHA256SUMS"), []byte(idx.String()), 0o644); err != nil {
		return err
	}
	var b strings.Builder
	for _, f := range dropped {
		b.WriteString(f + "\n")
	}
	return os.WriteFile(filepath.Join(*to, "dropped"), []byte(b.String()), 0o644)
}
