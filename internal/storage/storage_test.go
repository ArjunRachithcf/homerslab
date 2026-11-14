package storage_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/storage"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// A local folder is taken only where it is a folder of its own, reached without a symlink, and holds nothing of the
// machine's or HomersLab's: not /, nor /etc, /proc, /sys, /dev, the engine's socket, HomersLab's state, nor a folder
// that holds one of them (#28).
func TestAFolderIsTakenOnlyWhereItHoldsNothingOfTheMachines(t *testing.T) {
	testenv.Isolate(t)
	root := t.TempDir()
	for _, d := range []string{"srv/media/Films", "srv/My Music", "data/real", "etc", "var/lib/homerslab-agent", "var/lib/docker", "run"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(root, "srv/a-file"), nil, 0o644)
	_ = os.Symlink(filepath.Join(root, "etc"), filepath.Join(root, "srv/to-etc"))
	_ = os.Symlink("real", filepath.Join(root, "data/link"))
	_ = os.Symlink(filepath.Join(root, "data"), filepath.Join(root, "mnt"))

	for _, ok := range []string{"/srv/media", "/srv/media/Films", "/srv/My Music", "/data/real"} {
		if err := storage.CheckFolder(root, ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for p, want := range map[string]error{
		"srv/media":                storage.ErrPath, // not absolute
		"/srv/media/":              storage.ErrPath, // not clean
		"/srv/../etc":              storage.ErrPath,
		"/srv/./media":             storage.ErrPath,
		"/srv/media\nservices:":    storage.ErrPath,
		"/srv/${HOME}":             storage.ErrPath, // compose would interpolate it
		"/srv/a:b":                 storage.ErrPath,
		"/":                        storage.ErrForbidden,
		"/etc":                     storage.ErrForbidden,
		"/etc/ssl":                 storage.ErrForbidden,
		"/proc":                    storage.ErrForbidden,
		"/sys/fs":                  storage.ErrForbidden,
		"/dev":                     storage.ErrForbidden,
		"/run":                     storage.ErrForbidden, // the engine's socket
		"/var/run":                 storage.ErrForbidden,
		"/var/lib/docker":          storage.ErrForbidden,
		"/var/lib/homerslab":       storage.ErrForbidden,
		"/var/lib/homerslab-agent": storage.ErrForbidden,
		"/var/lib/homerslab-other": storage.ErrForbidden,
		"/var/lib":                 storage.ErrForbidden, // holds HomersLab's state
		"/var":                     storage.ErrForbidden,
		"/srv/to-etc":              storage.ErrSymlink,
		"/data/link":               storage.ErrSymlink,
		"/mnt/real":                storage.ErrSymlink, // a symlink on the way
		"/srv/a-file":              storage.ErrNotFolder,
		"/srv/nothing":             storage.ErrNotFolder,
	} {
		if err := storage.CheckFolder(root, p); !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", p, err, want)
		}
	}
}

// An app's path in its container is an absolute path of plain names: nothing that leaves it, nor that compose would
// read as a variable.
func TestAnAppsPathForALocationIsPlain(t *testing.T) {
	testenv.Isolate(t)
	for _, ok := range []string{"/media", "/data/films", "/srv/my_music-2"} {
		if !storage.ValidTarget(ok) {
			t.Errorf("target %q refused", ok)
		}
	}
	for _, bad := range []string{"", "/", "media", "/media/", "/a/../b", "/a/./b", "/a b", "/$x", "/a,b", "/a:b"} {
		if storage.ValidTarget(bad) {
			t.Errorf("target %q taken", bad)
		}
	}
}
