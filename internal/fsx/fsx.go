// Package fsx writes files that a power cut leaves whole: the machine's records - a person's
// account, their security keys, a laptop's firmware - are read at the next boot, and a file cut
// half way through it is worse than the one it replaced.
package fsx

import (
	"os"
	"path/filepath"
)

// WriteFile replaces path with b, mode mode, so that whatever happens a reader finds the old file
// or the new one: written beside it, synced, renamed over it, and its directory synced so the
// new name outlives a power cut too. Its directory is made when missing.
func WriteFile(path string, b []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // gone once renamed; left only by a failure
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
