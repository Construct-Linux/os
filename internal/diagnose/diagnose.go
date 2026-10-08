// Package diagnose gathers what a running machine says about itself - each program's answer a
// file - into one archive, and reads from those answers what to look at first: devices with no
// driver, firmware not found, errors by unit, failed logins. construct diagnose says which files
// there are and in what order; this runs them, writes them and reads them.
package diagnose

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"time"
)

// Probe is one file of the archive and what writes it.
type Probe struct {
	File string
	Run  func(ctx context.Context) ([]byte, error)
}

// Command is a probe whose file is a program's output, standard error too: what it could not read
// is part of the answer.
func Command(file string, args ...string) Probe {
	return Probe{File: file, Run: func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	}}
}

// probeTimeout bounds each probe: fwupd or a sensor that hangs costs one file, not the archive.
const probeTimeout = time.Minute

// Gather runs each probe, one after the other, and answers their files. A probe that fails keeps
// what it wrote and says why at its end; the rest go on.
func Gather(ctx context.Context, probes []Probe) map[string][]byte {
	files := make(map[string][]byte, len(probes))
	for _, p := range probes {
		files[p.File] = run(ctx, p)
	}
	return files
}

func run(ctx context.Context, p Probe) []byte {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := p.Run(ctx)
	if err != nil {
		out = append(out, fmt.Sprintf("\n[construct diagnose: %s]\n", err)...)
	}
	return out
}

// WriteArchive writes files under dir in a gzipped tar at path, which must not exist, readable by
// its owner alone; an archive it could not finish is removed, not left half written.
func WriteArchive(path, dir string, files map[string][]byte) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	now := time.Now()
	for _, name := range slices.Sorted(maps.Keys(files)) {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: dir + "/" + name, Mode: 0o644, Size: int64(len(body)), ModTime: now}); err != nil {
			f.Close()
			return err
		}
		if _, err := tw.Write(body); err != nil {
			f.Close()
			return err
		}
	}
	for _, c := range []io.Closer{tw, zw} {
		if err := c.Close(); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
