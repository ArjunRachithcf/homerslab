package agent

import (
	"encoding/json"
	"maps"
	"net/http"
	"path/filepath"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/ArjunRachithcf/homerslab/internal/appunit"
	"github.com/ArjunRachithcf/homerslab/internal/storage"
)

// Storage locations on this node (#28; story 48; ADR 0003), which the manager adds once and apps' plans name: so far a
// local folder; network shares reached over the mesh come with #28's second part.
//   - A folder is taken only where storage.CheckFolder passes: a folder of its own, reached without a symlink, holding
//     nothing of the machine's or HomersLab's. It is never relabelled or changed.
//   - An app's compose file binds the location's folder into the service that asked, read-only unless its plan says
//     otherwise.

// StorageRequest is a storage location as the manager adds it.
type StorageRequest struct {
	Kind string `json:"kind"` // folder
	Path string `json:"path,omitempty"`
}

// storageState is a location as the agent keeps it, in agent.json.
type storageState struct {
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
}

// StorageStatus is a location as the agent has it: Source is where apps' compose files bind it from.
type StorageStatus struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	Source string `json:"source"`
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

func (s *storageState) status(id string) StorageStatus {
	return StorageStatus{ID: id, Kind: s.Kind, Path: s.Path, Source: s.Path}
}

// mount is how an app's service sees the location: a bind of its folder.
func (s *storageState) mount(id string, as AppStorage) appunit.Mount {
	return appunit.Mount{Location: id, Service: as.Service, Source: s.status(id).Source, Target: as.Path, ReadOnly: as.ReadOnly}
}

// checkStorage is what is wrong with a request for location id, by its code and status, or "". The caller holds a.mu.
func (a *Agent) checkStorage(id string, r StorageRequest) (string, int) {
	if r.Kind != "folder" {
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

// putStorage adds a storage location, or puts one back as it was added (a manager that didn't keep it, retrying). A
// location no app uses yet can become another (the manager kept none); one an app uses can't.
func (a *Agent) putStorage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req StorageRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields() // a field this agent doesn't take is a kind of location it doesn't have
	if err := dec.Decode(&req); err != nil || !storage.ID.MatchString(id) {
		fail(w, http.StatusBadRequest, "bad-storage")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	next := &storageState{Kind: req.Kind, Path: req.Path}
	prev := a.state.Storage[id]
	if prev != nil && *prev == *next {
		writeJSON(w, http.StatusOK, next.status(id)) // as it was added, whatever its folder is like now
		return
	}
	code, status := a.checkStorage(id, req)
	if code == "" && prev != nil && a.inUse(id) {
		code, status = "storage-exists", http.StatusConflict
	}
	if code != "" {
		a.log.Warn("refused a storage location", "location", id, "why", code)
		fail(w, status, code)
		return
	}
	if a.state.Storage == nil {
		a.state.Storage = map[string]*storageState{}
	}
	a.state.Storage[id] = next
	if err := a.save(); err != nil {
		if prev == nil {
			delete(a.state.Storage, id)
		} else {
			a.state.Storage[id] = prev
		}
		a.log.Error("keeping a storage location", "location", id, "err", err)
		fail(w, http.StatusInternalServerError, "state-unsaved")
		return
	}
	a.log.Info("a storage location is added", "location", id, "kind", next.Kind)
	writeJSON(w, http.StatusOK, next.status(id))
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
