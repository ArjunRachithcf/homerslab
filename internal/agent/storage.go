package agent

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ArjunRachithcf/homerslab/internal/appunit"
	"github.com/ArjunRachithcf/homerslab/internal/host"
	"github.com/ArjunRachithcf/homerslab/internal/storage"
)

// Storage locations on this node (#28; story 48; ADR 0003): a local folder, or a network share reached over the mesh,
// which the manager adds once and apps' plans name.
//   - A folder is taken only where storage.CheckFolder passes: a folder of its own, reached without a symlink, holding
//     nothing of the machine's or HomersLab's. It is never relabelled or changed.
//   - A share's server is a mesh peer: an IPv4 address outside the LAN's prefixes, which the kernel routes through the
//     mesh's interface. The firewall table then keeps this machine's packets to it on the mesh, before the share is
//     ever mounted, and whatever the route says later (at boot, before the mesh is up). Its own unit mounts it with the
//     standard mount command (ADR 0014); an SMB share's credentials are a systemd credential the agent seals (ADR
//     0011), which systemd decrypts for the mount alone. They are never kept in the clear, logged or answered.
//   - An app's compose file binds the location's folder, or its share's mount, into the service that asked, read-only
//     unless its plan says otherwise; its unit starts only once its shares are mounted.

// StorageRequest is a storage location as the manager adds it. Username and Password (an SMB share's) are secret:
// they are sealed, and never logged, kept in the clear or answered.
type StorageRequest struct {
	Kind     string           `json:"kind"` // folder or share
	Path     string           `json:"path,omitempty"`
	Protocol storage.Protocol `json:"protocol,omitempty"`
	Server   string           `json:"server,omitempty"`
	Share    string           `json:"share,omitempty"` // an SMB share's name, or an NFS export's path
	Username string           `json:"username,omitempty"`
	Password string           `json:"password,omitempty"`
}

// storageState is a location as the agent keeps it, in agent.json. None of it is secret.
type storageState struct {
	Kind     string           `json:"kind"`
	Path     string           `json:"path,omitempty"`
	Protocol storage.Protocol `json:"protocol,omitempty"`
	Server   netip.Addr       `json:"server,omitzero"`
	Share    string           `json:"share,omitempty"`
}

// StorageStatus is a location as the agent has it: Source is where apps' compose files bind it from, Unit a share's
// unit. None of it is secret.
type StorageStatus struct {
	ID       string           `json:"id"`
	Kind     string           `json:"kind"`
	Path     string           `json:"path,omitempty"`
	Protocol storage.Protocol `json:"protocol,omitempty"`
	Server   string           `json:"server,omitempty"`
	Share    string           `json:"share,omitempty"`
	Source   string           `json:"source"`
	Unit     string           `json:"unit,omitempty"`
}

// StorageList is every location on this node.
type StorageList struct {
	Locations []StorageStatus `json:"locations"`
}

// AppStorage is a storage location an app's plan names: its service sees it at Path.
type AppStorage struct {
	Location string `json:"location"`
	Service  string `json:"service"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly"`
}

// mountWait is how long a share's mount takes at most, as its unit's TimeoutStartSec says, and a little.
const mountWait = 75 * time.Second

func (s *storageState) status(id string) StorageStatus {
	st := StorageStatus{ID: id, Kind: s.Kind, Path: s.Path, Protocol: s.Protocol, Share: s.Share, Source: s.Path}
	if s.Kind == "share" {
		st.Server, st.Source, st.Unit = s.Server.String(), storage.MountPoint(id), storage.Unit(id)
	}
	return st
}

// mount is how an app's service sees the location: a bind of its folder or of its share's mount.
func (s *storageState) mount(id string, as AppStorage) appunit.Mount {
	st := s.status(id)
	return appunit.Mount{Location: id, Service: as.Service, Source: st.Source, Target: as.Path, ReadOnly: as.ReadOnly, Unit: st.Unit}
}

// storageCredential is where the agent keeps an SMB share's sealed credential.
func (a *Agent) storageCredential(id string) string {
	return filepath.Join(a.cfg.StateDir, "storage", id+".cred")
}

// share is a share as its unit mounts it.
func (a *Agent) share(id string, s *storageState) storage.Share {
	sh := storage.Share{ID: id, Protocol: s.Protocol, Server: s.Server, Share: s.Share, SELinux: a.selinux()}
	if s.Protocol == storage.SMB {
		sh.Credential = a.storageCredential(id)
	}
	return sh
}

// checkStorage is what is wrong with a request for location id, by its code and status, or "": its fields alone, then
// a folder on the machine, or a share's server on the mesh. The caller holds a.mu.
func (a *Agent) checkStorage(ctx context.Context, id string, r StorageRequest) (string, int) {
	switch r.Kind {
	case "folder":
		if r.Protocol != "" || r.Server != "" || r.Share != "" || r.Username != "" || r.Password != "" {
			return "bad-storage", http.StatusBadRequest
		}
		if err := a.checkFolder(r.Path); err != nil {
			return err.Error(), http.StatusUnprocessableEntity
		}
		// No location inside another: an app that writes to one could put a symlink where the other's folder was.
		for other, s := range a.state.Storage {
			if other != id && s.Kind == "folder" && storage.Nested(s.Path, r.Path) {
				return "storage-overlaps", http.StatusConflict
			}
		}
		return "", 0
	case "share":
	default:
		return "bad-storage", http.StatusBadRequest
	}
	switch {
	case r.Path != "":
		return "bad-storage", http.StatusBadRequest
	case r.Protocol == storage.SMB && (!storage.ShareName.MatchString(r.Share) || !storage.Username.MatchString(r.Username) || !storage.ValidPassword(r.Password)):
		return "bad-share", http.StatusBadRequest
	case r.Protocol == storage.NFS && (!storage.ValidExport(r.Share) || r.Username != "" || r.Password != ""):
		return "bad-share", http.StatusBadRequest
	case r.Protocol != storage.SMB && r.Protocol != storage.NFS:
		return "bad-share", http.StatusBadRequest
	}
	server, err := storage.CheckServer(r.Server)
	if err != nil {
		return "bad-server", http.StatusBadRequest
	}
	if len(a.state.Mesh) == 0 {
		return "no-mesh", http.StatusConflict
	}
	if !a.onTheMesh(ctx, server) {
		return "not-on-the-mesh", http.StatusUnprocessableEntity
	}
	return "", 0
}

// checkFolder says whether a folder may be a location, now: storage.CheckFolder, outside the agent's own state, and on
// SELinux labelled so that every app reads it at its own level. Nothing is relabelled: the admin labels it.
func (a *Agent) checkFolder(p string) error {
	if err := storage.CheckFolder(a.cfg.Root, p, a.cfg.StateDir); err != nil {
		return err
	}
	if a.selinux() {
		if l, err := a.cfg.FolderLabel(filepath.Join(a.cfg.Root, p)); err != nil || !storage.ReadableLabel(l) {
			return storage.ErrLabel
		}
	}
	return nil
}

// folderLabel is a folder's SELinux label, from its security.selinux attribute.
func folderLabel(path string) (string, error) {
	b := make([]byte, 256)
	n, err := unix.Lgetxattr(path, "security.selinux", b)
	if err != nil {
		return "", err
	}
	return string(b[:n]), nil
}

// inUse says whether an app's compose file binds the location. The caller holds a.mu.
func (a *Agent) inUse(id string) bool {
	for _, app := range a.state.Apps {
		if slices.ContainsFunc(app.Mounts, func(m appunit.Mount) bool { return m.Location == id }) {
			return true
		}
	}
	return false
}

// onTheMesh says whether a server is a mesh peer: outside the LAN's prefixes, and routed through the mesh's interface
// by the kernel. The caller holds a.mu.
func (a *Agent) onTheMesh(ctx context.Context, server netip.Addr) bool {
	if n, err := a.cfg.Network(); err == nil {
		a.network = n
	}
	for _, p := range a.network.Prefixes {
		if p.Contains(server) {
			return false
		}
	}
	ctx, cancel := context.WithTimeout(ctx, hostWait)
	defer cancel()
	out, err := a.cfg.Run(ctx, host.Cmd{Name: "ip", Args: []string{"-j", "route", "get", server.String()}})
	var routes []struct {
		Dev string `json:"dev"`
	}
	if err != nil || json.Unmarshal(out, &routes) != nil || len(routes) != 1 {
		a.log.Warn("asking the kernel for the route to a share's server", "server", server, "err", err)
		return false
	}
	return slices.Contains(a.state.Mesh, routes[0].Dev)
}

// putStorage adds a storage location, or puts one back as it was added (a manager that didn't keep it, retrying; an SMB
// share's new password). A folder no app uses yet can become another (the manager kept none); a share, or a location an
// app uses, can't.
func (a *Agent) putStorage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req StorageRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || !storage.ID.MatchString(id) {
		fail(w, http.StatusBadRequest, "bad-storage")
		return
	}
	a.installs.Lock()
	defer a.installs.Unlock()
	ctx := r.Context()
	a.mu.Lock()
	next := &storageState{Kind: req.Kind, Path: req.Path, Protocol: req.Protocol, Share: req.Share}
	if req.Kind == "share" {
		next.Server, _ = storage.CheckServer(req.Server)
	}
	prev := a.state.Storage[id]
	if prev != nil && *prev == *next && next.Kind == "folder" {
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, next.status(id)) // as it was added, whatever its folder is like now
		return
	}
	code, status := a.checkStorage(ctx, id, req)
	if code == "" && prev != nil && *prev != *next && (a.inUse(id) || prev.Kind != "folder" || next.Kind != "folder") {
		code, status = "storage-exists", http.StatusConflict
	}
	if code != "" {
		a.mu.Unlock()
		a.log.Warn("refused a storage location", "location", id, "why", code)
		fail(w, status, code)
		return
	}
	if a.state.Storage == nil {
		a.state.Storage = map[string]*storageState{}
	}
	a.state.Storage[id] = next
	err := a.save()
	if err == nil && next.Kind == "share" {
		// The share's server is kept to the mesh before the share is ever mounted.
		err = a.reconcile(ctx, true)
	}
	a.mu.Unlock()
	if err == nil && next.Kind == "share" {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mountWait)
		err = a.mountShare(ctx, id, next, req)
		cancel()
		if err != nil {
			a.log.Error("mounting a share", "location", id, "err", err)
			a.forgetShare(context.WithoutCancel(ctx), id, prev)
			fail(w, http.StatusBadGateway, "mount-failed")
			return
		}
	}
	if err != nil {
		a.log.Error("keeping a storage location", "location", id, "err", err)
		a.forgetShare(context.WithoutCancel(ctx), id, prev)
		fail(w, http.StatusInternalServerError, "state-unsaved")
		return
	}
	a.log.Info("a storage location is added", "location", id, "kind", next.Kind)
	writeJSON(w, http.StatusOK, next.status(id))
}

// mountShare seals an SMB share's credentials and has its unit mount it, waiting for the mount.
func (a *Agent) mountShare(ctx context.Context, id string, s *storageState, req StorageRequest) error {
	if s.Protocol == storage.SMB {
		cred := a.storageCredential(id)
		if err := os.MkdirAll(filepath.Dir(cred), 0o700); err != nil {
			return err
		}
		if err := a.sealAt(ctx, cred, storage.Credential(id), storage.CredentialFile(req.Username, req.Password)); err != nil {
			return err
		}
	}
	unit := path.Join(UnitsDir, storage.Unit(id))
	if err := a.writeAt(unit, storage.ShareUnit(a.share(id, s))); err != nil {
		return err
	}
	for _, step := range [][]string{{"systemctl", "enable", "/" + unit}, {"systemctl", "daemon-reload"}, {"systemctl", "restart", storage.Unit(id)}} {
		if err := a.run(ctx, step[0], step[1:]...); err != nil {
			return err
		}
	}
	return nil
}

// forgetShare puts a location back as it was before a request that failed: a new share's unit stopped and removed with
// its credential, and the table without its server.
func (a *Agent) forgetShare(ctx context.Context, id string, prev *storageState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if prev != nil {
		a.state.Storage[id] = prev
	} else {
		delete(a.state.Storage, id)
		_ = a.run(ctx, "systemctl", "disable", "--now", storage.Unit(id))
		for _, f := range []string{filepath.Join(a.cfg.Root, UnitsDir, storage.Unit(id)), a.storageCredential(id)} {
			if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
				a.log.Error("removing a share that didn't mount", "file", f, "err", err)
			}
		}
	}
	if err := a.save(); err != nil {
		a.log.Error("saving the agent's state", "err", err)
	}
	if err := a.reconcile(ctx, true); err != nil {
		a.log.Error("the firewall table, after a share that didn't mount", "err", err)
	}
}

func (a *Agent) getStorage(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := StorageList{Locations: []StorageStatus{}}
	for _, id := range slices.Sorted(maps.Keys(a.state.Storage)) {
		out.Locations = append(out.Locations, a.state.Storage[id].status(id))
	}
	writeJSON(w, http.StatusOK, out)
}

// shareServers are the shares' servers, which the firewall table keeps to the mesh. The caller holds a.mu.
func (a *Agent) shareServers() []netip.Addr {
	var out []netip.Addr
	for _, s := range a.state.Storage {
		if s.Kind == "share" {
			out = append(out, s.Server)
		}
	}
	return out
}

// appMounts are the mounts an app's request names, or the code that refuses it: a location the agent doesn't have, a
// folder that no longer passes its checks (a symlink put in its way since it was added, say), or a mount the agent's
// files couldn't name. The caller holds a.mu.
func (a *Agent) appMounts(r AppRequest) ([]appunit.Mount, string) {
	var out []appunit.Mount
	for _, as := range r.Storage {
		s := a.state.Storage[as.Location]
		if s == nil {
			return nil, "no-such-storage"
		}
		if s.Kind == "folder" {
			if err := a.checkFolder(s.Path); err != nil {
				return nil, err.Error()
			}
		}
		m := s.mount(as.Location, as)
		if !m.Checked() {
			return nil, "bad-storage"
		}
		out = append(out, m)
	}
	return out, ""
}
