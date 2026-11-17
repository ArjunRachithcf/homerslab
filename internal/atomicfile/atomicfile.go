// Package atomicfile writes the agent's state files whole or not at all, for root only, and on the disk before the
// write returns: the file's data is synced before it takes the name, and its folder after, so a power cut leaves the
// old file or the new one, never an empty or half-written one. An error means the old file is still there.
package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// syncFile is fsync; tests replace it (SetSync).
var syncFile = (*os.File).Sync

// SetSync replaces fsync for a test, and returns what puts it back.
func SetSync(f func(*os.File) error) func() {
	old := syncFile
	syncFile = f
	return func() { syncFile = old }
}

// Write replaces path with b (mode 0600, its folder 0700 when it makes one).
func Write(path string, b []byte) error { return WriteMode(path, b, 0o600, 0o700) }

// WriteMode is Write for a file other programs read: mode is the file's, dirMode its folder's when it makes one.
func WriteMode(path string, b []byte, mode, dirMode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(mode) // a leftover .new may have had another mode
	}
	if err = errors.Join(err, finish(f)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return rename(tmp, path)
}

// Commit gives a file another program wrote at tmp (systemd-creds) the name tmp has without its ".new", with the
// same guarantees as Write: its data is synced first, its folder after. An error removes tmp and leaves the old file.
func Commit(tmp string) error {
	f, err := os.OpenFile(tmp, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err = finish(f); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return rename(tmp, strings.TrimSuffix(tmp, ".new"))
}

// finish syncs f's data and closes it.
func finish(f *os.File) error {
	return errors.Join(syncFile(f), f.Close())
}

func rename(tmp, path string) error {
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// The folder's entry, so the new name survives a power cut. The new file is in place by now, so a failure here
	// isn't the write's: the caller has it, and the old file is gone either way.
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = syncFile(d)
		_ = d.Close()
	}
	return nil
}
