// Package storage is what HomersLab takes of a storage location (#28; story 48; ADR 0003): a local folder, or a network
// share reached over the mesh, which apps may use, read-only by default for media. No free text of an admin's or a
// catalog's reaches a file the agent writes: a location's id, a folder's path, a share's server, name and export, and an
// app's path in its container are each checked against a pattern first.
//
// A share is mounted by a unit of its own with the standard mount command (ADR 0014), its credentials a systemd
// credential the agent sealed (ADR 0011), which systemd decrypts for the mount alone: never in an environment, a log,
// an answer or a file the agent writes in the clear.
package storage

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ArjunRachithcf/homerslab/internal/text"
)

// ID is a storage location's id, as a unit, a credential and a folder are named after it.
var ID = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// Protocol is a network share's: SMB or NFS.
type Protocol string

const (
	SMB Protocol = "smb"
	NFS Protocol = "nfs"
)

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

// CheckServer is a share's server: an IPv4 address of a host, never a name, which could resolve elsewhere later.
func CheckServer(s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() || a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalUnicast() ||
		a == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return netip.Addr{}, errors.New("bad-server")
	}
	return a, nil
}

// ShareName is an SMB share's name; Username the account an SMB share is opened with.
var (
	ShareName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)
	Username  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)
)

// exportSegment is one name of an NFS export's path, or of an app's path in its container.
var exportSegment = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$`)

func plainPath(p string, allowRoot bool) bool {
	if p == "/" {
		return allowRoot
	}
	if !strings.HasPrefix(p, "/") || len(p) > 256 {
		return false
	}
	for _, n := range strings.Split(p[1:], "/") {
		if n == "." || n == ".." || !exportSegment.MatchString(n) {
			return false
		}
	}
	return true
}

// ValidExport says whether p is an NFS export's path: / or a path of plain names.
func ValidExport(p string) bool { return plainPath(p, true) }

// ValidTarget says whether p is where a service sees a location: an absolute path of plain names in its container.
func ValidTarget(p string) bool { return plainPath(p, false) }

// ValidPassword says whether p is a password a share's credential file can hold: one line of printable text.
func ValidPassword(p string) bool { return p != "" && len(p) <= 256 && text.OneLine(p) }

// Share is a network share as the agent mounts it.
type Share struct {
	ID       string
	Protocol Protocol
	Server   netip.Addr
	// Share is an SMB share's name, or an NFS export's path.
	Share string
	// Credential is where the agent keeps the sealed credential an SMB share is opened with.
	Credential string
	// SELinux says whether the machine has SELinux: the mount then labels the share's files for containers, at no
	// level, so every app given the location reads it at its own (no file on the share is relabelled).
	SELinux bool
}

// Valid says whether every field of the share is one its unit may name.
func (s Share) Valid() bool {
	switch {
	case !ID.MatchString(s.ID) || !s.Server.Is4() || strings.ContainsAny(s.Credential, " \t\n\r%,"):
		return false
	case s.Protocol == SMB:
		return ShareName.MatchString(s.Share) && strings.HasPrefix(s.Credential, "/")
	case s.Protocol == NFS:
		return ValidExport(s.Share) && s.Credential == ""
	}
	return false
}

// Unit is a share's unit's name; Credential its credential's name; MountPoint where it is mounted, which apps' compose
// files bind.
func Unit(id string) string       { return "homerslab-storage-" + id + ".service" }
func Credential(id string) string { return "storage-" + id }
func MountPoint(id string) string { return "/var/lib/homerslab-storage/" + id }

// CredentialFile is the SMB credential file's body, as mount.cifs reads it.
func CredentialFile(user, password string) []byte {
	return []byte("username=" + user + "\npassword=" + password + "\n")
}

// ShareUnit is a share's unit: the standard mount command, run once, at boot and when the agent restarts it, until the
// mesh lets it reach the share. The firewall table lets the machine reach the share's server through the mesh only.
func ShareUnit(s Share) []byte {
	var b strings.Builder
	w := func(f string, args ...any) { fmt.Fprintf(&b, f+"\n", args...) }
	id := s.ID
	if !s.Valid() {
		s, id = Share{Protocol: "refused"}, "unknown"
	}
	at := MountPoint(id)
	opts := "nosuid,nodev,noexec"
	if s.SELinux {
		opts += ",context=system_u:object_r:container_file_t:s0"
	}
	// No free text: the location's id, the server's address, and the share's name or export, each checked.
	w("# Written by HomersLab's agent. Never edit by hand.")
	w("# Storage location %s: a network share on the mesh, mounted by the standard mount command (ADR 0014). The firewall", id)
	w("# table lets this machine reach its server through the mesh only, never the LAN.")
	if s.Protocol == SMB {
		w("# Its credentials are a systemd credential the agent sealed (ADR 0011), decrypted for the mount alone.")
	}
	w("[Unit]")
	w("Description=HomersLab storage location %s", id)
	w("Documentation=https://github.com/ArjunRachithcf/homerslab")
	w("After=network-online.target netbird.service homerslab-agent.service")
	w("Wants=network-online.target")
	w("")
	w("[Service]")
	w("Type=oneshot")
	w("RemainAfterExit=yes")
	w("StateDirectory=%s", strings.TrimPrefix(at, "/var/lib/"))
	w("StateDirectoryMode=0700")
	switch s.Protocol {
	case SMB:
		w("LoadCredentialEncrypted=%s:%s", Credential(id), s.Credential)
		w("ExecStart=/usr/bin/mount -t cifs -o credentials=%%d/%s,%s //%s/%s %s", Credential(id), opts, s.Server, s.Share, at)
	case NFS:
		w("ExecStart=/usr/bin/mount -t nfs -o %s %s:%s %s", opts, s.Server, s.Share, at)
	default:
		w("ExecStart=/usr/bin/false") // never a share the agent didn't check
	}
	w("ExecStop=/usr/bin/umount %s", at)
	w("TimeoutStartSec=60s")
	w("# Until the mesh is up at boot, or the share answers again.")
	w("Restart=on-failure")
	w("RestartSec=30s")
	w("")
	w("[Install]")
	w("WantedBy=multi-user.target")
	return []byte(b.String())
}
