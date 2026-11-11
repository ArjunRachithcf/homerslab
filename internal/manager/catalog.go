package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/manager/plan"
	"github.com/ArjunRachithcf/homerslab/internal/manager/store"
)

// catalogInfo is a catalog as the Store names it.
type catalogInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Publisher string `json:"publisher"`
}

// storeApp is one app in the Store's list.
type storeApp struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Purpose     string               `json:"purpose"`
	Description string               `json:"description"`
	Category    string               `json:"category"`
	Tier        catalog.Tier         `json:"tier"`
	SignIn      catalog.SignInMethod `json:"signIn"` // the method it prefers
	Icon        string               `json:"icon"`
}

type screenshot struct {
	URL     string `json:"url"`
	Caption string `json:"caption"`
}

func (m *Manager) info() catalogInfo {
	return catalogInfo{ID: m.catalog.ID, Name: m.catalog.Name, Publisher: m.catalog.Publisher}
}

func appURL(id string) string { return "/api/v1/catalog/apps/" + id }

func (m *Manager) getCatalog(w http.ResponseWriter, _ *http.Request) {
	apps := []storeApp{}
	for _, a := range m.catalog.Apps {
		apps = append(apps, storeApp{ID: a.ID, Name: a.Name, Purpose: a.Purpose, Description: a.Description, Category: a.Category,
			Tier: a.Tier, SignIn: a.SignIn[0].Method, Icon: appURL(a.ID) + "/icon"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalog": m.info(), "categories": m.catalog.Categories, "apps": apps})
}

func (m *Manager) getCatalogApp(w http.ResponseWriter, r *http.Request) {
	a, ok := m.catalog.App(r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, "no-such-app")
		return
	}
	shots := []screenshot{}
	for i, s := range a.Screenshots {
		shots = append(shots, screenshot{URL: fmt.Sprintf("%s/screenshots/%d", appURL(a.ID), i), Caption: s.Caption})
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalog": m.info(), "app": a, "icon": appURL(a.ID) + "/icon", "screenshots": shots})
}

func (m *Manager) getIcon(w http.ResponseWriter, r *http.Request) {
	a, ok := m.catalog.App(r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, "no-such-app")
		return
	}
	m.picture(w, a, a.Icon)
}

func (m *Manager) getScreenshot(w http.ResponseWriter, r *http.Request) {
	a, ok := m.catalog.App(r.PathValue("id"))
	n, err := strconv.Atoi(r.PathValue("n"))
	if !ok || err != nil || n < 0 || n >= len(a.Screenshots) || r.PathValue("n") != strconv.Itoa(n) {
		fail(w, http.StatusNotFound, "no-such-picture")
		return
	}
	m.picture(w, a, a.Screenshots[n].File)
}

var pictureTypes = map[string]string{".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp"}

// picture serves one of an app's pictures, as a picture only: an SVG opened on its own runs no script and loads
// nothing.
func (m *Manager) picture(w http.ResponseWriter, a catalog.App, file string) {
	typ := pictureTypes[strings.ToLower(path.Ext(file))]
	b, err := fs.ReadFile(m.catalogFS, path.Join("apps", a.ID, file))
	if typ == "" || err != nil {
		fail(w, http.StatusNotFound, "no-such-picture")
		return
	}
	h := w.Header()
	h.Set("Content-Type", typ)
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(b)
}

// The ways planning can be refused.
var (
	errNotSetUp = errors.New("household-not-set-up")
	errAgent    = errors.New("agent-unreachable")
)

// providers are the identity providers' reports of the sign-in methods they provide (spec: Adapter contracts).
var providers = map[string]plan.Provider{
	"authentik": {ID: "authentik", Name: "Authentik", Provides: []catalog.SignInMethod{catalog.OIDC, catalog.LDAP, catalog.Guard}},
	"kanidm":    {ID: "kanidm", Name: "Kanidm", Provides: []catalog.SignInMethod{catalog.OIDC, catalog.LDAP, catalog.Guard}},
}

// household is the household's state as the planner needs it: first-time setup's answers so far, and the primary as
// its agent reports it. Where setup's step isn't built yet, its default stands in: the domain from the admin's mail
// address, NetBird alone, and Authentik, the one identity provider until Kanidm's slice (spec: Design decisions,
// round 4; #17).
func (m *Manager) household(ctx context.Context) (plan.Household, error) {
	a, err := m.db.Answers(ctx)
	if err != nil {
		return plan.Household{}, err
	}
	if a["household"] == "" || a["mail"] == "" {
		return plan.Household{}, errNotSetUp
	}
	facts, err := m.agent.node(ctx)
	if err != nil {
		m.log.Warn("asking the agent for its facts", "err", err)
		return plan.Household{}, errAgent
	}
	return plan.Household{
		Name:     a["household"],
		Domain:   strings.ToLower(a["mail"][strings.LastIndex(a["mail"], "@")+1:]),
		Meshes:   []string{"NetBird"},
		Provider: providers["authentik"],
		People:   []plan.Person{{Name: a["name"], Role: "admin"}},
		Primary:  plan.Node{Facts: facts, Engine: catalog.Docker},
	}, nil
}

func (m *Manager) newPlan(w http.ResponseWriter, r *http.Request) {
	// An app's id is its catalog's (#89): the request names both, the official catalog when it leaves it out.
	var body struct {
		Catalog string `json:"catalog"`
		App     string `json:"app"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	if body.Catalog != "" && body.Catalog != m.catalog.ID {
		fail(w, http.StatusNotFound, "no-such-catalog")
		return
	}
	a, ok := m.catalog.App(body.App)
	if !ok {
		fail(w, http.StatusNotFound, "no-such-app")
		return
	}
	h, err := m.household(r.Context())
	switch {
	case errors.Is(err, errNotSetUp):
		fail(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, errAgent):
		fail(w, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		m.internal(w, "reading the household", err)
		return
	}
	project, err := m.agent.readProject(r.Context(), engine.ProjectPrefix+a.ID, a.Compose)
	var refused *agentRefused
	switch {
	case errors.As(err, &refused):
		m.log.Error("the agent can't read an app's compose file", "app", a.ID, "err", err)
		fail(w, http.StatusUnprocessableEntity, refused.Code)
		return
	case err != nil:
		m.log.Warn("asking the agent to read a compose file", "err", err)
		fail(w, http.StatusServiceUnavailable, errAgent.Error())
		return
	}
	p, err := plan.Make(a, plan.From{ID: m.catalog.ID, Publisher: m.catalog.Publisher}, project, h, m.cfg.Now())
	if errors.Is(err, plan.ErrNoSignInMethod) || errors.Is(err, plan.ErrDefinition) {
		m.log.Error("planning an app", "app", a.ID, "err", err)
		fail(w, http.StatusUnprocessableEntity, strings.SplitN(err.Error(), ":", 2)[0])
		return
	}
	if err != nil {
		m.internal(w, "planning", err)
		return
	}
	b, err := json.Marshal(p)
	if err != nil {
		m.internal(w, "planning", err)
		return
	}
	kept, made, err := m.db.SavePlan(r.Context(), p.ID, a.ID, b, p.MadeAt)
	if err != nil {
		m.internal(w, "keeping a plan", err)
		return
	}
	status := http.StatusOK // the same plan, made earlier
	if made {
		status = http.StatusCreated
		m.log.Info("made an install plan", "plan", p.ID, "app", a.ID)
	}
	writeRaw(w, status, kept)
}

func (m *Manager) getPlan(w http.ResponseWriter, r *http.Request) {
	b, err := m.db.Plan(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "no-such-plan")
		return
	}
	if err != nil {
		m.internal(w, "reading a plan", err)
		return
	}
	writeRaw(w, http.StatusOK, b)
}

// writeRaw answers with JSON kept as it was made.
func writeRaw(w http.ResponseWriter, status int, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}
