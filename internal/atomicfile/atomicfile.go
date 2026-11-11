// Package atomicfile writes the agent's state files whole or not at all, for root only, and on the disk before the
// write returns: the file's data is synced before it takes the name, and its folder after, so a power cut leaves the
// old file or the new one, never an empty or half-written one. An error means the old file is still there.
package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
)

// syncFile is fsync; tests replace it.
var syncFile = (*os.File).Sync

// Write replaces path with b (mode 0600, its folder 0700 when it makes one).
func Write(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(0o600) // a leftover .new may have had another mode
	}
	if err == nil {
		err = syncFile(f)
	}
	if err = errors.Join(err, f.Close()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// The folder's entry, so the new name survives a power cut. The new file is in place by now, so a failure here
	// isn't the write's: the caller has it, and the old file is gone either way.
	if d, err := os.Open(dir); err == nil {
		_ = syncFile(d)
		_ = d.Close()
	}
	return nil
}
