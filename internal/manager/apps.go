package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/appunit"
	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/manager/plan"
	"github.com/ArjunRachithcf/homerslab/internal/manager/store"
)

// Applying an install plan (#19; spec: The install plan). Applying takes the plan's id and does what the plan says,
// nothing more: the plan must still be the one the household gets now (the same definition, from the same catalog,
// into the same household, which is what its id hashes), so a stale plan is refused rather than applied in part. Then:
//   - the identity provider gets the app's guard, for its name and its audience;
//   - the agent gets the app: the plan's name, upstream, secrets and health checks, and the catalog's compose file whose
//     digest the plan carries;
//   - the household keeps that the plan was applied.
//
// A plan with an extra is a risky change: it needs the admin's passkey (step-up, #25), so it is refused until then.
// Apps that sign people in themselves (OIDC, LDAP) come with #20; only guarded apps are applied so far.

// The ways applying is refused.
var (
	errStalePlan   = errors.New("stale-plan")
	errStepUp      = errors.New("step-up-required")
	errNotBuilt    = errors.New("sign-in-not-built")
	errNoSignInYet = errors.New("sign-in-not-ready")
)

// appView is an installed app as the household sees it: what its plan says, and how the agent finds it.
type appView struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Purpose   string              `json:"purpose"`
	Host      string              `json:"host"`
	URL       string              `json:"url"`
	Plan      string              `json:"plan"`
	AppliedAt time.Time           `json:"appliedAt"`
	Node      string              `json:"node"`
	Health    string              `json:"health"`
	Active    bool                `json:"active"`
	Checks    []agent.CheckResult `json:"checks"`
	// Pending is what of the app's wiring the agent is still putting right (firewall, edge).
	Pending []string `json:"pending,omitempty"`
}

// siteURL is an app's one URL, https://<name>[:port] (ADR 0021).
func siteURL(ans map[string]string, host string) string {
	u := "https://" + host
	if p := householdPort(ans); p != 443 {
		u += ":" + strconv.Itoa(p)
	}
	return u
}

// roles are the roles in a plan's default audience.
func roles(audience string) []idp.Role {
	switch audience {
	case "everyone":
		return []idp.Role{idp.Admin, idp.Adult, idp.Kid}
	case "adults":
		return []idp.Role{idp.Admin, idp.Adult}
	case "admins":
		return []idp.Role{idp.Admin}
	}
	return nil
}

// current is the plan the household gets now for the app a plan installs, so the two can be compared by id.
func (m *Manager) current(ctx context.Context, p plan.Plan) (catalog.App, error) {
	a, ok := m.catalog.App(p.App.ID)
	if !ok || p.App.Catalog != m.catalog.ID || a.Digest != p.App.Definition {
		return catalog.App{}, errStalePlan
	}
	h, err := m.household(ctx)
	if err != nil {
		return catalog.App{}, err
	}
	project, err := m.agent.readProject(ctx, engine.ProjectPrefix+a.ID, a.Compose)
	if err != nil {
		return catalog.App{}, err
	}
	now, err := plan.Make(a, plan.From{ID: m.catalog.ID, Publisher: m.catalog.Publisher}, project, h, m.cfg.Now())
	if err != nil {
		return catalog.App{}, err
	}
	if now.ID != p.ID {
		return catalog.App{}, errStalePlan
	}
	return a, nil
}

func (m *Manager) applyPlan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	b, err := m.db.Plan(ctx, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "no-such-plan")
		return
	}
	if err != nil {
		m.internal(w, "reading a plan", err)
		return
	}
	var p plan.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		m.internal(w, "reading a plan", err)
		return
	}
	switch {
	case p.Kind != "install":
		fail(w, http.StatusUnprocessableEntity, "not-an-install")
		return
	case p.Risky:
		fail(w, http.StatusForbidden, errStepUp.Error())
		return
	case p.SignIn.Method != catalog.Guard:
		fail(w, http.StatusUnprocessableEntity, errNotBuilt.Error())
		return
	}
	a, err := m.current(ctx, p)
	var refused *agentRefused
	switch {
	case errors.Is(err, errStalePlan):
		fail(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, errNotSetUp):
		fail(w, http.StatusConflict, err.Error())
		return
	case errors.As(err, &refused):
		fail(w, http.StatusUnprocessableEntity, refused.Code)
		return
	case err != nil:
		m.log.Warn("making the plan again, to apply it", "plan", p.ID, "err", err)
		fail(w, http.StatusServiceUnavailable, errAgent.Error())
		return
	}
	ans, err := m.db.Answers(ctx)
	if err != nil {
		m.internal(w, "reading the answers", err)
		return
	}
	provider, err := m.provider(ctx, ans)
	if err != nil {
		m.log.Warn("the household account, to guard an app", "err", err)
		fail(w, http.StatusConflict, errNoSignInYet.Error())
		return
	}
	req := agent.AppRequest{Plan: p.ID, Product: p.App.Name, Compose: string(a.Compose), Name: p.Name.Host, Service: a.Names.Service,
		Port: a.Names.Port, Guard: true, Secrets: []appunit.Secret{}, Health: []agent.AppHealth{}}
	for _, s := range a.Secrets {
		req.Secrets = append(req.Secrets, appunit.Secret{Name: s.Name, UID: s.UID})
	}
	for _, h := range a.Health {
		ah := agent.AppHealth{Service: h.Service, Compose: h.Compose}
		if h.HTTP != nil {
			ah.Port, ah.Path = h.HTTP.Port, h.HTTP.Path
		}
		req.Health = append(req.Health, ah)
	}
	// The agent checks the app first, so a refused plan changes nothing anywhere; then the guard, before the edge
	// serves the app's name; then the app.
	agentFailed := func(err error) {
		var e *agentError
		if errors.As(err, &e) && e.Status/100 == 4 {
			m.log.Error("the agent refused an app", "app", a.ID, "plan", p.ID, "code", e.Code)
			fail(w, http.StatusUnprocessableEntity, e.Code)
			return
		}
		m.log.Error("installing an app", "app", a.ID, "err", err)
		fail(w, http.StatusServiceUnavailable, errAgent.Error())
	}
	if err := m.agent.install(ctx, a.ID, req, true, nil); err != nil {
		agentFailed(err)
		return
	}
	guard := idp.Guard{Slug: "homerslab-" + a.ID, Name: p.App.Name, URL: siteURL(ans, p.Name.Host), Roles: roles(p.Audience.Default)}
	if err := provider.EnsureGuard(ctx, guard); err != nil {
		m.log.Error("making an app's guard", "app", a.ID, "err", err)
		fail(w, http.StatusBadGateway, "guard-failed")
		return
	}
	var st agent.AppStatus
	if err := m.agent.install(ctx, a.ID, req, false, &st); err != nil {
		agentFailed(err)
		return
	}
	if err := m.db.SaveApp(ctx, a.ID, p.ID, m.cfg.Now()); err != nil {
		m.internal(w, "keeping an app", err)
		return
	}
	m.log.Info("applied an install plan", "plan", p.ID, "app", a.ID, "host", p.Name.Host)
	if err := m.RefreshHome(ctx); err != nil {
		m.log.Warn("Home's projection, after an install", "err", err)
	}
	writeJSON(w, http.StatusAccepted, m.view(store.App{ID: a.ID, Plan: p.ID, AppliedAt: m.cfg.Now().UTC()}, p, ans, &st))
}

// view is an app as the household sees it, from its plan and the agent's status (nil while the agent doesn't say).
func (m *Manager) view(a store.App, p plan.Plan, ans map[string]string, st *agent.AppStatus) appView {
	v := appView{ID: a.ID, Name: p.App.Name, Purpose: p.App.Purpose, Host: p.Name.Host, URL: siteURL(ans, p.Name.Host), Plan: a.Plan,
		AppliedAt: a.AppliedAt, Node: p.Placement.Node, Health: "unknown", Checks: []agent.CheckResult{}}
	if st != nil {
		v.Health, v.Active, v.Pending = st.Health, st.Active, st.Pending
		if st.Checks != nil {
			v.Checks = st.Checks
		}
	}
	return v
}

// getApps is the household's apps, each with its health as the agent finds it now, and what runs without its unit.
func (m *Manager) getApps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	apps, err := m.db.Apps(ctx)
	if err != nil {
		m.internal(w, "reading the apps", err)
		return
	}
	ans, err := m.db.Answers(ctx)
	if err != nil {
		m.internal(w, "reading the answers", err)
		return
	}
	var st agent.AppsStatus
	if err := m.agent.with(m.agent.status).call(ctx, http.MethodGet, "/v1/apps", nil, &st); err != nil {
		m.log.Warn("asking the agent for the apps", "err", err)
	}
	byID := map[string]*agent.AppStatus{}
	for i := range st.Apps {
		byID[st.Apps[i].ID] = &st.Apps[i]
	}
	out := []appView{}
	for _, a := range apps {
		b, err := m.db.Plan(ctx, a.Plan)
		if err != nil {
			m.internal(w, "reading a plan", err)
			return
		}
		var p plan.Plan
		if err := json.Unmarshal(b, &p); err != nil {
			m.internal(w, "reading a plan", err)
			return
		}
		out = append(out, m.view(a, p, ans, byID[a.ID]))
	}
	drift := st.Drift
	if drift == nil {
		drift = []agent.AppDrift{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": out, "drift": drift})
}

// install hands the agent an app, or only asks whether it would install it (check), waiting for the engine's answer,
// the compose read and the rest of the install.
func (a *agentClient) install(ctx context.Context, id string, req agent.AppRequest, check bool, out *agent.AppStatus) error {
	ctx, cancel := context.WithTimeout(ctx, agent.InstallDeadline+5*time.Second)
	defer cancel()
	p := "/v1/apps/" + id
	if check {
		p += "?check=true"
		return a.with(a.installs).call(ctx, http.MethodPut, p, req, nil)
	}
	return a.with(a.installs).call(ctx, http.MethodPut, p, req, out)
}

// with is the client, asking through c.
func (a *agentClient) with(c *http.Client) *agentClient {
	b := *a
	b.http = c
	return &b
}
