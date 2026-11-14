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

// plainName says whether n is one name of a folder's path as a media library's folders are called, in any script
// (Médias, Björk), and nothing compose would interpolate ($), nor a quote, an escape, a specifier (%) or a separator of
// a volume's short syntax (:) or of an option (,). Not hidden, nor . or ..
func plainName(n string) bool {
	return n != "" && len(n) <= 255 && !strings.HasPrefix(n, ".") && strings.TrimSpace(n) == n && text.OneLine(n) &&
		!strings.ContainsAny(n, "$\\\"`%:,")
}

// forbidden are the machine's own folders, and those of its services and HomersLab's (all under /var/lib, the engine's
// socket under /run): a location is none of them, inside none, and holds none. /home is the household's people's own,
// and the agent's unit can't see it (ProtectHome).
var forbidden = []string{"/etc", "/proc", "/sys", "/dev", "/run", "/var/run", "/boot", "/efi", "/root", "/home", "/tmp", "/var/tmp",
	"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/snap", "/var/lib", "/var/log", "/var/spool", "/var/cache", "/var/mail"}

// CheckFolder says whether p, under the machine's root directory root ("/" on a real machine), may be a storage
// location: an absolute, clean path of plain names, outside the machine's and HomersLab's folders (and also's: the
// agent's own state, wherever it is), holding none of them, that is a folder reached without a symlink.
func CheckFolder(root, p string, also ...string) error {
	if err := Allowed(p); err != nil {
		return err
	}
	for _, f := range also {
		if Nested(p, f) {
			return ErrForbidden
		}
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
	for _, n := range strings.Split(p[1:], "/") {
		if !plainName(n) {
			return ErrPath
		}
	}
	for _, f := range forbidden {
		if Nested(p, f) {
			return ErrForbidden
		}
	}
	return nil
}

// Nested says whether one path is the other, or holds it.
func Nested(a, b string) bool {
	return a == b || strings.HasPrefix(a, strings.TrimSuffix(b, "/")+"/") || strings.HasPrefix(b, strings.TrimSuffix(a, "/")+"/")
}

// ErrLabel is a folder SELinux keeps containers from: only one labelled for containers at no level (container_file_t,
// or container_ro_file_t, s0) is read by every app at its own level.
var ErrLabel = errors.New("selinux-label")

// ReadableLabel says whether a folder's SELinux label (user:role:type:level) is one every app's container reads.
func ReadableLabel(label string) bool {
	f := strings.Split(strings.TrimRight(label, "\x00"), ":")
	return len(f) == 4 && (f[2] == "container_file_t" || f[2] == "container_ro_file_t") && f[3] == "s0"
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
