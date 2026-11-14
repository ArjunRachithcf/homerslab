package storage_test

import (
	"bytes"
	"errors"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/storage"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

var update = flag.Bool("update", false, "write the golden renders anew")

// A local folder is taken only where it is a folder of its own, reached without a symlink, and holds nothing of the
// machine's or HomersLab's: not /, nor /etc, /proc, /sys, /dev, the engine's socket, HomersLab's state, nor a folder
// that holds one of them (#28).
func TestAFolderIsTakenOnlyWhereItHoldsNothingOfTheMachines(t *testing.T) {
	testenv.Isolate(t)
	root := t.TempDir()
	for _, d := range []string{"srv/media/Films", "srv/My Music", "srv/Médias/Björk", "data/real", "etc", "var/lib/homerslab-agent", "var/lib/docker", "run", "srv/state"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(root, "srv/a-file"), nil, 0o644)
	_ = os.Symlink(filepath.Join(root, "etc"), filepath.Join(root, "srv/to-etc"))
	_ = os.Symlink("real", filepath.Join(root, "data/link"))
	_ = os.Symlink(filepath.Join(root, "data"), filepath.Join(root, "mnt"))

	for _, ok := range []string{"/srv/media", "/srv/media/Films", "/srv/My Music", "/srv/Médias/Björk", "/data/real"} {
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
		"/srv/.hidden":             storage.ErrPath,
		"/srv/50%":                 storage.ErrPath, // a unit's specifier
		"/srv/a\"b":                storage.ErrPath,
		"/srv/ x":                  storage.ErrPath,
		"/srv/a\u2028b":            storage.ErrPath,
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
		"/var/lib/netbird":         storage.ErrForbidden, // the mesh's keys
		"/var/spool/cron":          storage.ErrForbidden, // root's crontabs
		"/home/rui":                storage.ErrForbidden,
		"/root/keys":               storage.ErrForbidden,
		"/srv/state":               storage.ErrForbidden, // the agent's state, wherever it is
		"/var/lib":                 storage.ErrForbidden, // holds HomersLab's state
		"/var":                     storage.ErrForbidden,
		"/srv/to-etc":              storage.ErrSymlink,
		"/data/link":               storage.ErrSymlink,
		"/mnt/real":                storage.ErrSymlink, // a symlink on the way
		"/srv/a-file":              storage.ErrNotFolder,
		"/srv/nothing":             storage.ErrNotFolder,
	} {
		if err := storage.CheckFolder(root, p, "/srv/state"); !errors.Is(err, want) {
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

// On SELinux, every app reads a location at its own level only when the folder is labelled for containers at none.
func TestOnlyAFolderLabelledForContainersAtNoLevelIsReadByEveryApp(t *testing.T) {
	testenv.Isolate(t)
	for label, ok := range map[string]bool{
		"system_u:object_r:container_file_t:s0":        true,
		"unconfined_u:object_r:container_ro_file_t:s0": true,
		"system_u:object_r:container_file_t:s0\x00":    true, // as the kernel ends the attribute
		"system_u:object_r:container_file_t:s0:c1,c2":  false,
		"system_u:object_r:var_t:s0":                   false,
		"unconfined_u:object_r:user_home_t:s0":         false,
		"":                                             false,
	} {
		if storage.ReadableLabel(label) != ok {
			t.Errorf("%q: %v", label, !ok)
		}
	}
}

// A share's server is an IPv4 address, never a name (which could resolve elsewhere later), and never one no share can
// be at; its name, its export, the user and the password are what a mount takes, and nothing that ends a line or an
// option.
func TestAShareIsTakenOnlyAsPlainValues(t *testing.T) {
	testenv.Isolate(t)
	for _, ok := range []string{"100.64.1.2", "100.92.17.5"} {
		if _, err := storage.CheckServer(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "nas.local", "127.0.0.1", "0.0.0.0", "224.0.0.1", "169.254.1.1", "255.255.255.255", "fd7a:115c:a1e0::1", "100.64.1.2:445"} {
		if _, err := storage.CheckServer(bad); err == nil {
			t.Errorf("server %q taken", bad)
		}
	}
	for _, ok := range []string{"media", "Films_2", "family.photos"} {
		if !storage.ShareName.MatchString(ok) {
			t.Errorf("share %q refused", ok)
		}
	}
	for _, bad := range []string{"", "C$", "a/b", "a,b", "a b", "-media", strings.Repeat("a", 81)} {
		if storage.ShareName.MatchString(bad) {
			t.Errorf("share %q taken", bad)
		}
	}
	for _, ok := range []string{"/", "/export/media", "/volume1/Films"} {
		if !storage.ValidExport(ok) {
			t.Errorf("export %q refused", ok)
		}
	}
	for _, bad := range []string{"", "export", "/export/../etc", "/a,b", "/a b", "/a/"} {
		if storage.ValidExport(bad) {
			t.Errorf("export %q taken", bad)
		}
	}
	for _, bad := range []string{"", "a b", "a,b", "a\nb", "a=b", strings.Repeat("u", 65)} {
		if storage.Username.MatchString(bad) {
			t.Errorf("user %q taken", bad)
		}
	}
	if !storage.Username.MatchString("rui.ferreira") || !storage.ValidPassword("correct horse, battery=staple") {
		t.Error("a plain user or password refused")
	}
	for _, bad := range []string{"", "a\nusername=root", "a\rb", "a\x00b", strings.Repeat("p", 257)} {
		if storage.ValidPassword(bad) {
			t.Errorf("password %q taken", bad)
		}
	}
}

// Golden renders (spec: Testing Decisions): a share's unit, byte for byte. A deliberate change runs
// `go test ./internal/storage -update` and explains the diff.
func TestAShareIsMountedByItsOwnUnitAsItsGoldenRender(t *testing.T) {
	testenv.Isolate(t)
	shares := map[string]storage.Share{
		"smb.service": {ID: "films", Protocol: storage.SMB, Server: netip.MustParseAddr("100.64.1.2"), Share: "media",
			Credential: "/var/lib/homerslab-agent/storage/films.cred", SELinux: true},
		"nfs.service": {ID: "music", Protocol: storage.NFS, Server: netip.MustParseAddr("100.64.1.3"), Share: "/volume1/Music"},
	}
	for file, s := range shares {
		t.Run(file, func(t *testing.T) {
			got := storage.ShareUnit(s)
			path := filepath.Join("testdata", file)
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to write it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("the render differs from %s:\n%s", path, got)
			}
		})
	}
	// A share no check passed mounts nothing.
	bad := shares["smb.service"]
	bad.Share = "media /etc"
	if u := string(storage.ShareUnit(bad)); strings.Contains(u, "/usr/bin/mount") || !strings.Contains(u, "ExecStart=/usr/bin/false\n") {
		t.Errorf("an unchecked share's unit:\n%s", u)
	}
	// The credentials never reach the unit: it names the sealed file, which systemd decrypts for the mount only.
	u := string(storage.ShareUnit(shares["smb.service"]))
	if !strings.Contains(u, "LoadCredentialEncrypted=storage-films:/var/lib/homerslab-agent/storage/films.cred\n") ||
		!strings.Contains(u, "credentials=%d/storage-films,") {
		t.Errorf("the unit doesn't mount with the sealed credential:\n%s", u)
	}
}
