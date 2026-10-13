package main

import (
	"archive/tar"
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// layer is one source of files in the root, in the order they are laid down.
type layer interface {
	name() string
	walk(fn func(h *tar.Header, r io.Reader) error) error
}

type tarLayer struct{ file, prefix string }

func (l tarLayer) name() string { return l.file }

func (l tarLayer) walk(fn func(*tar.Header, io.Reader) error) error {
	f, err := os.Open(l.file)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(bufio.NewReaderSize(f, 1<<20))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", l.file, err)
		}
		h.Name = clean(path.Join(l.prefix, h.Name))
		if h.Name == "" {
			continue
		}
		if err := fn(h, tr); err != nil {
			return err
		}
	}
}

type dirLayer struct{ dir, prefix string }

func (l dirLayer) name() string { return l.dir }

func (l dirLayer) walk(fn func(*tar.Header, io.Reader) error) error {
	return filepath.WalkDir(l.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(l.dir, p)
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		// Everything in the root is root's, whoever built it, and writable by root alone: the
		// checkout's modes carry its umask.
		h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
		if h.Typeflag != tar.TypeSymlink {
			h.Mode &^= 0o022
		}
		h.Name = clean(path.Join(l.prefix, filepath.ToSlash(rel)))
		if !info.Mode().IsRegular() {
			return fn(h, nil)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		return fn(h, f)
	})
}

// keyDir holds the release key's public half at usr/share/construct/release.pgp (task usb).
var keyDir = filepath.Join(out, "release-key")

// cliDir holds cmd/construct at usr/bin/construct, which task tools builds for the root (the tools
// container that lays the root has no Go): a layer of its own.
var cliDir = filepath.Join(out, "construct")

func clean(name string) string {
	return strings.TrimPrefix(path.Clean("/"+name), "/")
}

// rootLayers is what the root is made of, in the order it is laid down; test adds what lets
// cmd/image test drive the session.
func rootLayers(test bool) []layer {
	layers := []layer{
		tarLayer{file: filepath.Join(out, "rootfs.tar")},
		dirLayer{dir: filepath.Join(out, "modules")},
		tarLayer{file: filepath.Join(out, "firmware", "firmware.tar"), prefix: "usr/lib/firmware"},
		dirLayer{dir: "image/rootfs"},
		dirLayer{dir: themeDir},
		dirLayer{dir: cliDir},
		dirLayer{dir: transfersDir},
	}
	// The key a release's signature is checked against, as task usb exported it from the signing
	// keyring: absent from a disk built without one, which then can verify no update.
	if _, err := os.Stat(keyDir); err == nil {
		layers = append(layers, dirLayer{dir: keyDir})
	}
	if test {
		layers = append(layers, dirLayer{dir: "image/test/rootfs"})
	}
	return layers
}

func disk(args []string) error {
	fl := flag.NewFlagSet("disk", flag.ExitOnError)
	// The ext4 image's size.
	size := "12G"
	test := fl.Bool("test", false, "add image/test/rootfs: what lets cmd/image test drive the session")
	_ = fl.Parse(args)

	stage := filepath.Join(out, "disk")
	root := filepath.Join(stage, "root")
	// The test disk is never offered an update.
	if _, err := bakedRoot(stage, root, rootLayers(*test), "", noUpdates); err != nil {
		return err
	}
	img := filepath.Join(out, "disk.raw")
	_ = os.Remove(img)
	cmd := exec.Command("mke2fs", "-q", "-t", "ext4", "-L", "root", "-d", root, img, size)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mke2fs: %w", err)
	}
	fmt.Println(img)
	return nil
}

// merge writes every layer into one tar, a name laid down by a later layer replacing the earlier
// one's, and every directory a file sits in present before it.
func merge(layers []layer, dst string) error {
	owner := make(map[string]int)
	// A directory's own header, from the layer that has it: a tar may list a directory's files
	// before the directory, and mke2fs needs the directory first.
	dirHdr := make(map[string]tar.Header)
	for i, l := range layers {
		if err := l.walk(func(h *tar.Header, _ io.Reader) error {
			owner[h.Name] = i
			// A directory keeps the first layer's header: the package that made it decides its
			// owner and mode, not an overlay that puts a file in it.
			if h.Typeflag == tar.TypeDir {
				if _, ok := dirHdr[h.Name]; !ok {
					dirHdr[h.Name] = *h
				}
			} else {
				delete(dirHdr, h.Name)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("%s: %w", l.name(), err)
		}
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	tw := tar.NewWriter(bw)
	written := map[string]bool{}
	var dir func(name string) error
	dir = func(name string) error {
		if name == "." || written[name] {
			return nil
		}
		if err := dir(path.Dir(name)); err != nil {
			return err
		}
		written[name] = true
		h, ok := dirHdr[name]
		if !ok {
			h = tar.Header{Typeflag: tar.TypeDir, Mode: 0o755}
		}
		h.Name = name + "/"
		return tw.WriteHeader(&h)
	}
	for i, l := range layers {
		if err := l.walk(func(h *tar.Header, r io.Reader) error {
			if owner[h.Name] != i {
				return nil
			}
			if h.Typeflag == tar.TypeDir {
				return dir(h.Name)
			}
			if err := dir(path.Dir(h.Name)); err != nil {
				return err
			}
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			if r != nil && h.Typeflag == tar.TypeReg {
				_, err := io.Copy(tw, r)
				return err
			}
			return nil
		}); err != nil {
			return fmt.Errorf("%s: %w", l.name(), err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return f.Close()
}
