package node

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Dir is the system root at a folder of this machine: "/" on a real node. Unlike os.DirFS, it can say how much space a
// filesystem has.
func Dir(path string) StatFS { return dir{FS: os.DirFS(path), root: path} }

type dir struct {
	fs.FS
	root string
}

// Statfs reads the filesystem of the nearest folder of name that exists: HomersLab's own may not exist yet.
func (d dir) Statfs(name string) (Space, error) {
	p := filepath.Join(d.root, filepath.FromSlash(name))
	for {
		var st syscall.Statfs_t
		err := syscall.Statfs(p, &st)
		if err == nil {
			return Space{FreeBytes: st.Bavail * uint64(st.Bsize), SizeBytes: st.Blocks * uint64(st.Bsize)}, nil
		}
		parent := filepath.Dir(p)
		if !errors.Is(err, fs.ErrNotExist) || parent == p || len(parent) < len(d.root) {
			return Space{}, err
		}
		p = parent
	}
}
