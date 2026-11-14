// Package storage is what HomersLab takes of a storage location (#28; story 48; ADR 0003): a local folder, which apps
// may use, read-only by default for media. Network shares reached over the mesh come with #28's second part. No free
// text of an admin's or a catalog's reaches a file the agent writes: a location's id, a folder's path and an app's path
// in its container are each checked against a pattern first.
package storage

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ArjunRachithcf/homerslab/internal/text"
)

// ID is a storage location's id, as a unit, a credential and a folder are named after it.
var ID = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// The ways a folder is refused.
var (
	ErrPath      = errors.New("bad-path")
	ErrForbidden = errors.New("forbidden-path")
	ErrSymlink   = errors.New("symlink-in-path")
	ErrNotFolder = errors.New("not-a-folder")
)

// segment is one name of a folder's path: what a media library's folders are called, and nothing compose would
// interpolate ($), nor a separator of a volume's short syntax (:) or of an option (,).
var segment = regexp.MustCompile(`^[A-Za-z0-9_+()' -][A-Za-z0-9._+()' -]{0,127}$`)

// forbidden are the machine's own folders and HomersLab's: a location is none of them, inside none, and holds none.
// The engine's socket is under /run (/var/run links there).
var forbidden = []string{"/etc", "/proc", "/sys", "/dev", "/run", "/var/run", "/boot", "/root", "/tmp", "/var/tmp", "/usr", "/bin",
	"/sbin", "/lib", "/lib64", "/var/log", "/var/lib/docker", "/var/lib/containers", "/var/lib/homerslab"}

// CheckFolder says whether p, under the machine's root directory root ("/" on a real machine), may be a storage
// location: an absolute, clean path of plain names, outside the machine's and HomersLab's folders, holding none of
// them, that is a folder reached without a symlink.
func CheckFolder(root, p string) error {
	if err := Allowed(p); err != nil {
		return err
	}
	at := root
	for _, n := range strings.Split(p[1:], "/") {
		at = filepath.Join(at, n)
		fi, err := os.Lstat(at)
		switch {
		case err != nil:
			return ErrNotFolder
		case fi.Mode()&os.ModeSymlink != 0:
			return ErrSymlink
		case !fi.IsDir():
			return ErrNotFolder
		}
	}
	return nil
}

// Allowed says whether p may name a storage location's folder, by its path alone: absolute, clean, plain names,
// outside the machine's and HomersLab's folders, holding none of them.
func Allowed(p string) error {
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p || len(p) > 512 || !text.OneLine(p) {
		return ErrPath
	}
	if p == "/" {
		return ErrForbidden
	}
	names := strings.Split(p[1:], "/")
	for _, n := range names {
		if n == "." || n == ".." || !segment.MatchString(n) {
			return ErrPath
		}
	}
	for _, f := range forbidden {
		// HomersLab's own folders are every /var/lib/homerslab*: the manager's and the agent's among them.
		if p == f || strings.HasPrefix(p, f+"/") || strings.HasPrefix(f, p+"/") || (f == "/var/lib/homerslab" && strings.HasPrefix(p, f)) {
			return ErrForbidden
		}
	}
	return nil
}

// targetSegment is one name of an app's path in its container.
var targetSegment = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$`)

// ValidTarget says whether p is where a service sees a location: an absolute path of plain names in its container.
func ValidTarget(p string) bool {
	if !strings.HasPrefix(p, "/") || p == "/" || len(p) > 256 {
		return false
	}
	for _, n := range strings.Split(p[1:], "/") {
		if n == "." || n == ".." || !targetSegment.MatchString(n) {
			return false
		}
	}
	return true
}
