package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Construct-Linux/os/internal/gpt"
)

// forge makes, from a release's stick, the sticks an update must refuse: its signature gone, its
// signature changed, its SHA256SUMS changed after it was signed, and its kernel image changed while
// SHA256SUMS still names the original. It edits the construct-update partition's FAT with mtools, in
// build/tools: no stick is built again.
func forge(args []string) error {
	fl := flag.NewFlagSet("forge", flag.ExitOnError)
	src := fl.String("stick", filepath.Join(out, "lifecycle", "v2.raw"), "the release's stick")
	_ = fl.Parse(args)

	offset, err := partitionOffset(*src, "construct-update")
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp(filepath.Dir(*src), "forge-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	names, err := mtools("mdir", "-b", "-i", fmt.Sprintf("%s@@%d", *src, offset), "::/")
	if err != nil {
		return err
	}
	efi := ""
	for _, n := range strings.Fields(names) {
		if strings.HasSuffix(n, ".efi") || strings.HasSuffix(n, ".EFI") {
			efi = strings.TrimPrefix(n, "::/")
		}
	}
	if efi == "" {
		return fmt.Errorf("%s's construct-update has no kernel image: %s", *src, names)
	}
	for _, v := range []struct {
		name string
		edit func(img string) error
	}{
		{"unsigned", func(img string) error {
			_, err := mtools("mdel", "-i", img, "::/SHA256SUMS.gpg")
			return err
		}},
		{"badsig", func(img string) error { return flipIn(img, "SHA256SUMS.gpg", work, 20) }},
		{"sums", func(img string) error { return flipIn(img, "SHA256SUMS", work, 3) }},
		{"uki", func(img string) error { return flipIn(img, efi, work, 1<<20) }},
	} {
		dst := strings.TrimSuffix(*src, ".raw") + "-" + v.name + ".raw"
		if err := copyCmd("cp", "--sparse=always", *src, dst); err != nil {
			return err
		}
		if err := v.edit(fmt.Sprintf("%s@@%d", dst, offset)); err != nil {
			return fmt.Errorf("%s: %w", v.name, err)
		}
		fmt.Println(dst)
	}
	return nil
}

// flipIn changes one byte, at, of the file name in the FAT image img.
func flipIn(img, name, work string, at int64) error {
	local := filepath.Join(work, filepath.Base(name))
	if _, err := mtools("mcopy", "-o", "-i", img, "::/"+name, local); err != nil {
		return err
	}
	b, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	if int64(len(b)) <= at {
		at = int64(len(b)) / 2
	}
	b[at] ^= 0x01
	if err := os.WriteFile(local, b, 0o644); err != nil {
		return err
	}
	_, err = mtools("mcopy", "-o", "-i", img, local, "::/"+name)
	return err
}

func mtools(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "MTOOLS_SKIP_CHECK=1")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, b)
	}
	return string(b), nil
}

// partitionOffset is the byte offset of the partition named name in the disk image img.
func partitionOffset(img, name string) (int64, error) {
	f, err := os.Open(img)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	t, err := gpt.Read(f)
	if err != nil {
		return 0, err
	}
	p, err := t.Named(name)
	if err != nil {
		return 0, err
	}
	return p.Offset(), nil
}
