package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Construct-Linux/os/internal/fsx"
)

// A machine follows a channel: stable, the default sysupdate component (usr/lib/sysupdate.d), or
// another the image has transfers for (usr/lib/sysupdate.<channel>.d, cmd/image transfers.go).
// construct-update.service reads which from channelFile: systemd-sysupdate's --component, or
// nothing for stable.

const (
	channelFile    = "etc/construct/channel"
	defaultChannel = "stable"
)

// channelCmd prints the channel the machine follows and those it could, or, given one, follows it
// from the next check on.
func channelCmd(args []string, w io.Writer, root string) error {
	available, err := channelsOf(root)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		fmt.Fprintf(w, "%s (of %s)\n", currentChannel(root), strings.Join(available, ", "))
		return nil
	}
	ch := args[0]
	if !slices.Contains(available, ch) {
		return fmt.Errorf("no channel %q: this release has %s", ch, strings.Join(available, ", "))
	}
	// The machine's own /etc is root's to write: said before a permission error says it less well.
	if root == "/" && os.Geteuid() != 0 {
		return errors.New("following a channel changes the machine's updates: sudo construct channel " + ch)
	}
	file := filepath.Join(root, channelFile)
	if ch == defaultChannel {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		// construct-update.service reads it at every check: one cut short by a power cut would
		// have the machine follow stable without a word.
		body := "# Written by construct channel: the channel construct-update.service follows.\n" +
			"SYSUPDATE_COMPONENT=--component=" + ch + "\n"
		if err := fsx.WriteFile(file, []byte(body), 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "following %s: the next check (construct-update.timer) takes its newest release\n", ch)
	return nil
}

// channelsOf are the channels the root has transfers for, stable first.
func channelsOf(root string) ([]string, error) {
	dirs, err := filepath.Glob(filepath.Join(root, "usr/lib/sysupdate.*.d"))
	if err != nil {
		return nil, err
	}
	var others []string
	for _, d := range dirs {
		others = append(others, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(d), "sysupdate."), ".d"))
	}
	sort.Strings(others)
	return append([]string{defaultChannel}, others...), nil
}

// currentChannel is the channel channelFile names, stable without it.
func currentChannel(root string) string {
	b, err := os.ReadFile(filepath.Join(root, channelFile))
	if err != nil {
		return defaultChannel
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "SYSUPDATE_COMPONENT=--component="); ok && v != "" {
			return v
		}
	}
	return defaultChannel
}
