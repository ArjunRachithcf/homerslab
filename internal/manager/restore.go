package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/agent"
	"github.com/ArjunRachithcf/homeward/internal/manager/plan"
	"github.com/ArjunRachithcf/homeward/internal/manager/store"
)

// An app's backups, and restoring it to an earlier point (#22; spec: Updates, removal, eject). Admin lists each app's
// backups as the points it can go back to. Going back is a plan, shown before anything happens; applying it is a risky
// change, so Admin refuses it until the admin's passkey is asked again (step-up, #25), and the machine's terminal,
// where root can already do anything, applies it on the control socket. The node's agent does the restore: a snapshot
// of the app as it is first, then the app's own volumes and dumps, a health check, and back to that snapshot when
// anything fails. The household keeps how the last restore of each app went, and hears of one that failed.

const (
	ansRestore  = "restore:" // an app's last restore, as JSON
	warnRestore = "restore:"
	listWait    = agent.ReadDeadline + 20*time.Second
)

// restoreView is how an app's last restore went: running, restored, rolled-back, rollback-failed, or refused (nothing
// was changed), with why in a word.
type restoreView struct {
	Plan   string              `json:"plan"`
	State  string              `json:"state"`
	Error  string              `json:"error,omitempty"`
	At     time.Time           `json:"at"`
	Before []agent.RestoreItem `json:"before,omitempty"`
}

// installed is the app as the household installed it, and what its install plan backs up, in the catalog's words.
func (m *Manager) installed(ctx context.Context, id string) (store.App, plan.Plan, []plan.Kept, error) {
	apps, err := m.db.Apps(ctx)
	if err != nil {
		return store.App{}, plan.Plan{}, nil, err
	}
	for _, a := range apps {
		if a.ID != id {
			continue
		}
		b, err := m.db.Plan(ctx, a.Plan)
		if err != nil {
			return a, plan.Plan{}, nil, err
		}
		var p plan.Plan
		if err := json.Unmarshal(b, &p); err != nil {
			return a, p, nil, err
		}
		// What the app's own install plan backs up: the plan it runs by, not the catalog's definition now.
		kept := []plan.Kept{}
		for _, d := range p.Backups.Data {
			kept = append(kept, plan.Kept{Data: d.Data, What: d.What, Kind: d.Kind})
		}
		return a, p, kept, nil
	}
	return store.App{}, plan.Plan{}, nil, store.ErrNotFound
}

// appBackups asks the node for the app's backups.
func (m *Manager) appBackups(ctx context.Context, id string) ([]plan.Backup, error) {
	var got agent.AppBackups
	// Within Admin's own time to answer: a target this slow to list is a failure to say, not a wait.
	ctx, cancel := context.WithTimeout(ctx, listWait)
	defer cancel()
	if err := m.agent.with(m.agent.backups).call(ctx, http.MethodGet, "/v1/apps/"+id+"/backups", nil, &got); err != nil {
		return nil, err
	}
	out := make([]plan.Backup, len(got.Snapshots))
	for i, s := range got.Snapshots {
		out[i] = plan.Backup{ID: s.ID, Time: s.Time, Data: s.Data, Size: s.Size, Incomplete: s.Incomplete}
	}
	return out, nil
}

// backupsOf answers a request with the app, what it keeps and its backups, or says why not and returns false.
func (m *Manager) backupsOf(w http.ResponseWriter, r *http.Request) (store.App, plan.Plan, []plan.Kept, []plan.Backup, bool) {
	a, p, kept, err := m.installed(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "no-such-app")
		return a, p, nil, nil, false
	}
	if err != nil {
		m.internal(w, "reading an app", err)
		return a, p, nil, nil, false
	}
	backups, err := m.appBackups(r.Context(), a.ID)
	var e *agentError
	switch {
	case errors.As(err, &e) && e.Status == http.StatusConflict:
		fail(w, http.StatusConflict, e.Code) // no-target
		return a, p, nil, nil, false
	case errors.As(err, &e) && e.Status == http.StatusBadGateway:
		fail(w, http.StatusBadGateway, e.Code) // target-failed
		return a, p, nil, nil, false
	case err != nil:
		m.log.Warn("asking the agent for an app's backups", "app", a.ID, "err", err)
		fail(w, http.StatusServiceUnavailable, errAgent.Error())
		return a, p, nil, nil, false
	}
	return a, p, kept, backups, true
}

// getAppBackups is `GET /api/v1/apps/{id}/backups`: the points the app can be restored to, latest first, each with its
// time and size, and how its last restore went.
func (m *Manager) getAppBackups(w http.ResponseWriter, r *http.Request) {
	a, _, kept, backups, ok := m.backupsOf(w, r)
	if !ok {
		return
	}
	out := map[string]any{"app": a.ID, "points": plan.Points(kept, backups)}
	var last restoreView
	if ans, err := m.db.Answers(r.Context()); err == nil && json.Unmarshal([]byte(ans[ansRestore+a.ID]), &last) == nil {
		// One said to be running that this manager isn't waiting for was started before it restarted: the agent
		// finished it on its own, and how is in its log and in the app's backups.
		if _, waiting := m.restoring.Load(a.ID); last.State == "running" && !waiting {
			last.State = "unknown"
		}
		out["restore"] = last
	}
	writeJSON(w, http.StatusOK, out)
}

// newRestorePlan is `POST /api/v1/apps/{id}/restore-plans`: the plan for taking the app back to one of its points. It
// changes nothing.
func (m *Manager) newRestorePlan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		At time.Time `json:"at"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.At.IsZero() {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	a, p, kept, backups, ok := m.backupsOf(w, r)
	if !ok {
		return
	}
	rp, err := plan.MakeRestore(plan.Restored{ID: a.ID, Name: p.App.Name, Installed: a.Plan}, kept, backups, body.At, m.cfg.Now())
	if errors.Is(err, plan.ErrNoSuchPoint) {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		m.internal(w, "planning a restore", err)
		return
	}
	b, err := json.Marshal(rp)
	if err != nil {
		m.internal(w, "planning a restore", err)
		return
	}
	saved, made, err := m.db.SavePlan(r.Context(), rp.ID, a.ID, b, rp.MadeAt)
	if err != nil {
		m.internal(w, "keeping a plan", err)
		return
	}
	code := http.StatusOK
	if made {
		code = http.StatusCreated
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(saved)
}

// applyRestore applies a restore plan. From Admin it needs the admin's passkey, asked again and bound to this plan
// (step-up, stepup.go). terminal says the request needs none: it came on the control socket, from the machine's
// terminal.
func (m *Manager) applyRestore(w http.ResponseWriter, r *http.Request, body []byte, terminal bool) {
	ctx := r.Context()
	var rp plan.Restore
	if err := json.Unmarshal(body, &rp); err != nil {
		m.internal(w, "reading a plan", err)
		return
	}
	// The plan must still be the app's: the same install, and each backup still the one the node holds for that thing
	// (the agent checks each snapshot again, under the app's own tags).
	a, _, _, err := m.installed(ctx, rp.App.ID)
	if errors.Is(err, store.ErrNotFound) || err == nil && a.Plan != rp.App.Installed {
		fail(w, http.StatusConflict, errStalePlan.Error())
		return
	}
	if err != nil {
		m.internal(w, "reading an app", err)
		return
	}
	if _, running := m.restoring.LoadOrStore(a.ID, true); running {
		fail(w, http.StatusConflict, "restore-running")
		return
	}
	if !terminal { // the step-up last, once the plan is known to be good and no restore of the app runs
		ap, err := m.reserveStepUp(r, "plan:"+rp.ID)
		if err != nil {
			m.restoring.Delete(a.ID)
			m.stepUpRefusal(w, err)
			return
		}
		ap.applied() // the restore starts now, and runs to its end
	}
	req := agent.RestoreRequest{}
	for _, it := range rp.Items {
		req.Items = append(req.Items, agent.RestoreItem{Data: it.Data, Snapshot: it.Snapshot})
	}
	m.keepRestore(ctx, a.ID, restoreView{Plan: rp.ID, State: "running"})
	m.log.Info("applying a restore plan", "plan", rp.ID, "app", a.ID, "to", rp.To)
	// The restore runs to its end whatever becomes of the request: stopping an app and restoring its data takes as long
	// as its data does.
	m.restores.Go(func() {
		defer m.restoring.Delete(a.ID)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agent.RestoreDeadline+time.Minute)
		defer cancel()
		var res agent.RestoreResult
		view := restoreView{Plan: rp.ID}
		err := m.agent.with(m.agent.backups).call(ctx, http.MethodPost, "/v1/apps/"+a.ID+"/restore", req, &res)
		var e *agentError
		switch {
		case errors.As(err, &e):
			view.State, view.Error = "refused", e.Code // nothing was changed
		case err != nil:
			m.log.Error("asking the agent to restore an app", "app", a.ID, "err", err)
			view.State, view.Error = "unknown", "agent-unreachable"
		default:
			view.State, view.Error, view.Before = res.State, res.Error, res.Before
		}
		m.keepRestore(ctx, a.ID, view)
		if view.State == "restored" {
			m.log.Info("an app is restored to an earlier point", "app", a.ID, "plan", rp.ID)
			_ = m.db.ResolveWarnings(ctx, warnRestore+a.ID, m.cfg.Now())
			return
		}
		m.log.Error("a restore didn't work", "app", a.ID, "plan", rp.ID, "state", view.State, "why", view.Error)
		m.warnOnce(ctx, warnRestore+a.ID, "restore-"+view.State+" "+view.Error)
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"plan": rp.ID, "app": a.ID, "state": "running"})
}

// keepRestore keeps how an app's last restore went.
func (m *Manager) keepRestore(ctx context.Context, app string, v restoreView) {
	v.At = m.cfg.Now().UTC()
	b, err := json.Marshal(v)
	if err == nil {
		err = m.db.SaveAnswers(ctx, map[string]string{ansRestore + app: string(b)})
	}
	if err != nil {
		m.log.Error("keeping how a restore went", "app", app, "err", err)
	}
}
