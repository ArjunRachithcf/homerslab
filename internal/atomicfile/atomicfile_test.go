package atomicfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/atomicfile"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

func TestAFileIsReplacedWholeForItsOwnerOnly(t *testing.T) {
	testenv.Isolate(t)
	path := filepath.Join(t.TempDir(), "state", "agent.json")
	for _, b := range []string{`{"a":1}`, `{"a":2}`} {
		if err := atomicfile.Write(path, []byte(b)); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != b {
			t.Errorf("read back %q, %v; want %q", got, err, b)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %o, want 600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
		t.Errorf("the folder's mode %o, want 700", fi.Mode().Perm())
	}
	if _, err := os.Stat(path + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the temporary file stayed: %v", err)
	}
}

// A write is on the disk before it counts: the file's data before the rename, and the folder's entry after it, so a
// power cut never leaves an empty or half-written file under the name. A write that failed left the old file.
func TestAWriteIsSyncedBeforeItCounts(t *testing.T) {
	testenv.Isolate(t)
	for _, failing := range []string{"file", "folder"} {
		t.Run(failing, func(t *testing.T) {
			testenv.Isolate(t)
			path := filepath.Join(t.TempDir(), "agent.json")
			if err := atomicfile.Write(path, []byte("old")); err != nil {
				t.Fatal(err)
			}
			var synced []string
			restore := atomicfile.SetSync(func(f *os.File) error {
				fi, err := f.Stat()
				if err != nil {
					return err
				}
				kind := "file"
				if fi.IsDir() {
					kind = "folder"
				}
				synced = append(synced, kind)
				if kind == failing {
					return errors.New("EIO, as the test asked")
				}
				return nil
			})
			defer restore()
			err := atomicfile.Write(path, []byte("new"))
			got, _ := os.ReadFile(path)
			// The folder's fsync comes after the rename: the new file is in place, and the write says so.
			if failing == "folder" && (err != nil || string(got) != "new") {
				t.Errorf("with the folder's fsync failing after the rename: %v, %q", err, got)
			}
			if failing == "file" && err == nil {
				t.Error("with the file's fsync failing, the write succeeded")
			}
			if failing == "file" {
				if got, _ := os.ReadFile(path); string(got) != "old" {
					t.Errorf("a write whose data never reached the disk replaced the file: %q", got)
				}
				if len(synced) != 1 {
					t.Errorf("synced %v", synced)
				}
			} else if len(synced) != 2 || synced[0] != "file" {
				t.Errorf("synced %v, want the file and then its folder", synced)
			}
		})
	}
}
