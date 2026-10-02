package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"debug/elf"
	"fmt"
	"io"
	"path"
	"strings"
)

// check reads the merged root and reports every ELF whose NEEDED libraries the dynamic loader
// would not find in it: a package whose dependency was not declared - a provider that publishes
// no so: names, a library in a private directory - shows up here, not as a crash at boot.
func check(root string) error {
	type obj struct {
		needed, runpath []string
	}
	files := map[string]bool{}
	links := map[string]string{}
	objs := map[string]obj{}
	err := tarLayer{file: root}.walk(func(h *tar.Header, r io.Reader) error {
		switch h.Typeflag {
		case tar.TypeSymlink:
			links[h.Name] = h.Linkname
			return nil
		case tar.TypeReg:
		default:
			return nil
		}
		files[h.Name] = true
		if h.Size < 64 {
			return nil
		}
		br := bufio.NewReader(r)
		if b, _ := br.Peek(4); !bytes.Equal(b, []byte(elf.ELFMAG)) {
			return nil
		}
		data, err := io.ReadAll(br)
		if err != nil {
			return err
		}
		f, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		defer f.Close()
		needed, _ := f.DynString(elf.DT_NEEDED)
		rp, _ := f.DynString(elf.DT_RUNPATH)
		rp2, _ := f.DynString(elf.DT_RPATH)
		var runpath []string
		for _, v := range append(rp, rp2...) {
			for _, d := range strings.Split(v, ":") {
				runpath = append(runpath, strings.ReplaceAll(d, "$ORIGIN", "/"+path.Dir(h.Name)))
			}
		}
		if len(needed) > 0 {
			objs[h.Name] = obj{needed, runpath}
		}
		return nil
	})
	if err != nil {
		return err
	}
	exists := func(name string) bool {
		for i := 0; i < 16; i++ {
			if files[name] {
				return true
			}
			t, ok := links[name]
			if !ok {
				return false
			}
			if !strings.HasPrefix(t, "/") {
				t = path.Join(path.Dir(name), t)
			}
			name = clean(t)
		}
		return false
	}
	// The directories some program in the root searches by its RUNPATH: a plugin it dlopens
	// (GNOME Software's) NEEDs a library from there without a RUNPATH of its own, and finds it
	// already loaded by its host.
	var hosts []string
	for _, o := range objs {
		hosts = append(hosts, o.runpath...)
	}
	bad := 0
	for name, o := range objs {
		dirs := append(append(o.runpath, "/usr/lib", "/lib", "/usr/lib64", "/lib64"), hosts...)
		// Mozilla's applications load their own directory's libraries first, in the order its
		// dependentlibs.list gives, before anything that NEEDs them.
		if files[path.Join(path.Dir(name), "dependentlibs.list")] {
			dirs = append(dirs, "/"+path.Dir(name))
		}
		for _, lib := range o.needed {
			found := false
			for _, d := range dirs {
				if exists(clean(path.Join(d, lib))) {
					found = true
					break
				}
			}
			if !found {
				fmt.Printf("%s: %s not found\n", name, lib)
				bad++
			}
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d unresolved libraries", bad)
	}
	return nil
}
