package manager

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/manager/store"
	"github.com/ArjunRachithcf/homerslab/internal/storage"
	"github.com/ArjunRachithcf/homerslab/internal/text"
)

// Storage locations (#28; story 48; ADR 0003). The admin adds each once, in Admin: so far a local folder on the
// primary (network shares over the mesh come with #28's second part). The agent checks it; the household keeps what the
// location is and whether the admin opted in to backing it up. Install plans name the location each app uses, and
// whether it only reads it.

// addLocation is a location as the admin adds it.
type addLocation struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // folder
	Path string `json:"path"`
}

func (m *Manager) storageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/storage", m.getStorage)
	mux.HandleFunc("POST /api/v1/storage", m.postStorage)
	mux.HandleFunc("PUT /api/v1/storage/{id}/backup", m.putStorageBackup)
}

func (m *Manager) getStorage(w http.ResponseWriter, r *http.Request) {
	ls, err := m.db.Locations(r.Context())
	if err != nil {
		m.internal(w, "reading the storage locations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"locations": ls})
}

func (m *Manager) postStorage(w http.ResponseWriter, r *http.Request) {
	var body addLocation
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	name := strings.TrimSpace(body.Name)
	switch {
	case !storage.ID.MatchString(body.ID):
		fail(w, http.StatusBadRequest, "bad-id")
		return
	case name == "" || len(name) > 60 || !text.OneLine(name):
		fail(w, http.StatusBadRequest, "bad-name")
		return
	}
	ctx := r.Context()
	ls, err := m.db.Locations(ctx)
	if err != nil {
		m.internal(w, "reading the storage locations", err)
		return
	}
	for _, l := range ls {
		if l.ID == body.ID {
			fail(w, http.StatusConflict, "storage-exists") // added once; never another under the same id
			return
		}
	}
	facts, err := m.agent.node(ctx)
	if err != nil {
		m.log.Warn("asking the agent for its facts", "err", err)
		fail(w, http.StatusServiceUnavailable, errAgent.Error())
		return
	}
	req := agent.StorageRequest{Kind: body.Kind, Path: body.Path}
	var st agent.StorageStatus
	if err := m.agent.call(ctx, http.MethodPut, "/v1/storage/"+body.ID, req, &st); err != nil {
		var e *agentError
		if errors.As(err, &e) && e.Status/100 == 4 {
			m.log.Warn("the agent refused a storage location", "location", body.ID, "code", e.Code)
			fail(w, e.Status, e.Code)
			return
		}
		m.log.Error("adding a storage location", "location", body.ID, "err", err)
		fail(w, http.StatusServiceUnavailable, errAgent.Error())
		return
	}
	l := store.Location{ID: body.ID, Name: name, Kind: st.Kind, Node: facts.Hostname, Path: st.Path, AddedAt: m.cfg.Now().UTC()}
	if err := m.db.AddLocation(ctx, l); errors.Is(err, store.ErrExists) {
		fail(w, http.StatusConflict, "storage-exists")
		return
	} else if err != nil {
		m.internal(w, "keeping a storage location", err)
		return
	}
	m.log.Info("a storage location is added", "location", l.ID, "kind", l.Kind)
	writeJSON(w, http.StatusCreated, l)
}

// putStorageBackup opts a location in to backups, or out: nothing is backed up unless the admin opts in, per location.
func (m *Manager) putStorageBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Backup *bool `json:"backup"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.Backup == nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	if err := m.db.SetLocationBackup(ctx, id, *body.Backup); errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "no-such-storage-location")
		return
	} else if err != nil {
		m.internal(w, "keeping a location's backup", err)
		return
	}
	ls, err := m.db.Locations(ctx)
	if err != nil {
		m.internal(w, "reading the storage locations", err)
		return
	}
	m.log.Info("a storage location's backup", "location", id, "backup", *body.Backup)
	for _, l := range ls {
		if l.ID == id {
			writeJSON(w, http.StatusOK, l)
			return
		}
	}
	fail(w, http.StatusNotFound, "no-such-storage-location")
}
