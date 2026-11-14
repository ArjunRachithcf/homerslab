package manager

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/home"
	"github.com/ArjunRachithcf/homerslab/internal/manager/plan"
)

// Home's projection (#21, ADR 0013): Home is a process of its own, unprivileged, which reads only what the manager
// writes for it here: the household's apps, each with its audience and its health, and Home's client of the sign-in.
// The manager writes it whole after every change that shows on Home, and every HomeEvery for the apps' health.
// Nothing in it is secret.

// HomeEvery is how often the manager refreshes the apps' health on Home.
const HomeEvery = 30 * time.Second

// RefreshHome writes Home's projection anew from the household's state and the agent's answers now. Before the
// household has a domain, Home has no name, and nothing is written.
func (m *Manager) RefreshHome(ctx context.Context) error {
	if m.cfg.Projection == "" {
		return nil
	}
	m.homeMu.Lock()
	defer m.homeMu.Unlock()
	ans, err := m.db.Answers(ctx)
	if err != nil || ans[ansDomain] == "" {
		return err
	}
	apps, err := m.db.Apps(ctx)
	if err != nil {
		return err
	}
	byID := map[string]*agent.AppStatus{}
	if len(apps) > 0 {
		var st agent.AppsStatus
		if err := m.agent.with(m.agent.status).call(ctx, http.MethodGet, "/v1/apps", nil, &st); err != nil {
			m.log.Warn("asking the agent for the apps' health, for Home", "err", err)
		}
		for i := range st.Apps {
			byID[st.Apps[i].ID] = &st.Apps[i]
		}
	}
	out := home.Projection{Household: ans["household"], Home: HomeURL(ans), Admin: AdminURL(ans), Apps: []home.App{}, MadeAt: m.cfg.Now().UTC()}
	if ans[ansHomeClientID] != "" && ans[ansHomeIssuer] != "" {
		out.SignIn = &home.SignIn{Issuer: ans[ansHomeIssuer], ClientID: ans[ansHomeClientID], Upstream: m.upstream()}
	}
	for _, a := range apps {
		// One app whose plan can't be read is left off Home; the others' health still reaches it.
		var p plan.Plan
		b, err := m.db.Plan(ctx, a.Plan)
		if err == nil {
			err = json.Unmarshal(b, &p)
		}
		if err != nil {
			m.log.Error("an app's plan, for Home", "app", a.ID, "plan", a.Plan, "err", err)
			continue
		}
		v := m.view(a, p, ans, byID[a.ID])
		h := home.App{ID: a.ID, Name: v.Name, Purpose: v.Purpose, URL: v.URL, Health: v.Health, Audience: []string{}}
		for _, r := range roles(p.Audience.Default) {
			h.Audience = append(h.Audience, string(r))
		}
		if d, ok := m.catalog.App(a.ID); ok {
			h.Category = d.Category
			if typ := pictureTypes[strings.ToLower(path.Ext(d.Icon))]; typ != "" {
				if b, err := fs.ReadFile(m.catalogFS, path.Join("apps", d.ID, d.Icon)); err == nil {
					h.Icon = &home.Icon{Type: typ, Data: b}
				}
			}
		}
		out.Apps = append(out.Apps, h)
	}
	// Written only when something changed, so Home reads it again only then.
	cmp := out
	cmp.MadeAt = time.Time{}
	sum, err := json.Marshal(cmp)
	if err != nil {
		return err
	}
	h := sha256.Sum256(sum)
	if h == m.homeSum {
		if _, err := os.Stat(m.cfg.Projection); err == nil {
			return nil
		}
	}
	if err := home.Write(m.cfg.Projection, out); err != nil {
		return err
	}
	m.homeSum = h
	return nil
}

// refreshHome keeps Home's projection current until ctx ends.
func (m *Manager) refreshHome(ctx context.Context) {
	every := m.cfg.HomeEvery
	if every == 0 {
		every = HomeEvery
	}
	for {
		if err := m.RefreshHome(ctx); err != nil && ctx.Err() == nil {
			m.log.Warn("Home's projection", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
