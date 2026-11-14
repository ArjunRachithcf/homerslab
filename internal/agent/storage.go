package agent

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"

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

// checkStorage is what is wrong with a request, by its code and status, or "".
func (a *Agent) checkStorage(r StorageRequest) (string, int) {
	if r.Kind != "folder" {
		return "bad-storage", http.StatusBadRequest
	}
	if err := storage.CheckFolder(a.cfg.Root, r.Path); err != nil {
		return err.Error(), http.StatusUnprocessableEntity
	}
	return "", 0
}

// putStorage adds a storage location, or puts one back as it was added. A location the agent has can't become another:
// an app may use it.
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
	code, status := a.checkStorage(req)
	next := &storageState{Kind: req.Kind, Path: req.Path}
	prev := a.state.Storage[id]
	if code == "" && prev != nil && *prev != *next {
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

// appMounts are the mounts an app's request names, or the code that refuses it: a location the agent doesn't have.
// The caller holds a.mu.
func (a *Agent) appMounts(r AppRequest) ([]appunit.Mount, string) {
	var out []appunit.Mount
	for _, as := range r.Storage {
		s := a.state.Storage[as.Location]
		if s == nil {
			return nil, "no-such-storage"
		}
		out = append(out, s.mount(as.Location, as))
	}
	return out, ""
}
