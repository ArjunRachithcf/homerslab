package manager

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/backup"
	"github.com/ArjunRachithcf/homerslab/internal/manager/store"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// The household's backups (#22; ADR 0010, amended for #88 and #90). The admin chooses the target; each node has its
// own repository there, with its own key. The primary's agent holds every node's key and the target's credentials as
// the manager's sealed credentials (for restore tests, moves and the recovery kit); each node's agent holds only its
// own key. Every hour the scheduler backs up the manager's own database and has each node's agent back up its apps,
// exactly as their plans said, and the sign-in's database; once a day it prunes. A failure is a warning, which the
// next run that works resolves.
//
// So far only the primary is a node (#38 adds others): its repository is "primary".

const (
	ansBackupTarget = "backupTarget"   // the target, as JSON: none of it is secret
	ansBackupAt     = "backupAt"       // when the last run started
	ansPrunedAt     = "backupPrunedAt" // when the last prune ran
	credTarget      = "manager-backup-target"
	warnBackup      = "backup:"
	primaryNode     = "primary"
)

// credKey is the sealed credential that holds a node's repository key, on the primary.
func credKey(node string) string { return "manager-backup-key-" + node }

// How often backups run and prune; a run a few minutes early still counts, as the scheduler's ticks drift.
const (
	backupEvery = time.Hour
	pruneEvery  = 24 * time.Hour
	early       = 5 * time.Minute
)

// targetRequest is the target the admin chose, and its credentials.
type targetRequest struct {
	Target      backup.Target      `json:"target"`
	Credentials backup.Credentials `json:"credentials"`
}

// putBackupTarget is `PUT /api/v1/backups/target`, on the control socket only, as the DNS token is: root on the primary
// can already do anything; from Admin, changing the target is a risky change (#22's second part). The node's key is
// made once and kept, so a new target holds the same node's repository under the same key.
func (m *Manager) putBackupTarget(w http.ResponseWriter, r *http.Request) {
	var req targetRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	if err := req.Target.Check(); err != nil {
		fail(w, http.StatusBadRequest, "bad-target")
		return
	}
	ctx := r.Context()
	key, err := m.agent.unseal(ctx, credKey(primaryNode))
	if errors.Is(err, errNotSealed) {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		key = mesh.Secret(hex.EncodeToString(b))
		err = m.agent.seal(ctx, credKey(primaryNode), key)
	}
	if err != nil {
		m.log.Error("the node's repository key", "err", err)
		fail(w, http.StatusServiceUnavailable, errAgent.Error())
		return
	}
	var out struct{ Repository string }
	err = m.agent.with(m.agent.backups).call(ctx, http.MethodPut, "/v1/backups",
		agent.BackupTarget{Node: primaryNode, Target: req.Target, Key: key.Reveal(), Credentials: req.Credentials}, &out)
	var e *agentError
	switch {
	case errors.As(err, &e) && e.Status/100 == 4:
		fail(w, e.Status, e.Code) // bad-target, wrong-key, backups-running
		return
	case errors.As(err, &e) && e.Status == http.StatusBadGateway:
		fail(w, http.StatusBadGateway, e.Code) // target-failed
		return
	case err != nil:
		m.log.Error("giving the agent the backup target", "err", err)
		fail(w, http.StatusServiceUnavailable, errAgent.Error())
		return
	}
	creds, err := json.Marshal(req.Credentials)
	if err == nil {
		err = m.agent.seal(ctx, credTarget, mesh.Secret(creds))
	}
	if err != nil {
		m.log.Error("keeping the target's credentials", "err", err)
		fail(w, http.StatusServiceUnavailable, errAgent.Error())
		return
	}
	target, _ := json.Marshal(req.Target)
	if err := m.db.SaveAnswers(ctx, map[string]string{ansBackupTarget: string(target)}); err != nil {
		m.internal(w, "keeping the backup target", err)
		return
	}
	m.log.Info("backups go to a new target", "repository", out.Repository)
	writeJSON(w, http.StatusOK, map[string]string{"repository": out.Repository})
}

// BackUpDue backs up the household when an hour has passed since the last run, and prunes once a day. The scheduler
// runs it beside its other jobs, so a long first backup holds none of them up.
func (m *Manager) BackUpDue(ctx context.Context) {
	if !m.backingUp.CompareAndSwap(false, true) {
		return
	}
	defer m.backingUp.Store(false)
	ans, err := m.db.Answers(ctx)
	if err != nil || ans[ansBackupTarget] == "" {
		return
	}
	now := m.cfg.Now()
	if !due(ans[ansBackupAt], now, backupEvery) {
		return
	}
	if err := m.db.SaveAnswers(ctx, map[string]string{ansBackupAt: now.UTC().Format(time.RFC3339)}); err != nil {
		m.log.Error("keeping when backups ran", "err", err)
		return
	}
	results := []agent.BackupResult{m.backUpManager(ctx)}
	var run agent.Backups
	if err := m.agent.with(m.agent.backups).call(ctx, http.MethodPost, "/v1/backups", nil, &run); err != nil {
		m.log.Error("the node's backups", "err", err)
		results = append(results, agent.BackupResult{Platform: "node", Data: primaryNode, Error: "agent-unreachable"})
	}
	results = append(results, run.Results...)
	failed := 0
	bySource := map[string][]agent.BackupResult{}
	var sources []string
	for _, r := range results {
		src := warnBackup + r.App + r.Platform
		if _, seen := bySource[src]; !seen {
			sources = append(sources, src)
		}
		bySource[src] = append(bySource[src], r)
	}
	for _, src := range sources {
		ok := true
		for _, r := range bySource[src] {
			if r.Error == "" {
				continue
			}
			ok = false
			failed++
			m.warnOnce(ctx, src, fmt.Sprintf("backup-failed %s %s", r.Data, r.Error))
		}
		if ok {
			_ = m.db.ResolveWarnings(ctx, src, now)
		}
	}
	m.log.Info("backed up the household", "items", len(results), "failed", failed)
	if !due(ans[ansPrunedAt], now, pruneEvery) {
		return
	}
	if err := m.agent.with(m.agent.backups).call(ctx, http.MethodPost, "/v1/backups/prune", nil, nil); err != nil {
		m.log.Error("pruning the node's repository", "err", err)
		m.warnOnce(ctx, warnBackup+"prune", "prune-failed")
		return
	}
	_ = m.db.ResolveWarnings(ctx, warnBackup+"prune", now)
	_ = m.db.SaveAnswers(ctx, map[string]string{ansPrunedAt: now.UTC().Format(time.RFC3339)})
}

// due says whether every (less a few minutes) has passed since last (RFC 3339, or "" for never).
func due(last string, now time.Time, every time.Duration) bool {
	at, err := time.Parse(time.RFC3339, last)
	return err != nil || now.Sub(at) >= every-early || now.Before(at)
}

// backUpManager hands the agent a consistent copy of the manager's own database.
func (m *Manager) backUpManager(ctx context.Context) agent.BackupResult {
	res := agent.BackupResult{Platform: "manager", Data: "manager:manager.db"}
	b, err := m.db.Snapshot(ctx)
	if err != nil {
		m.log.Error("copying the manager's database", "err", err)
		res.Error = "copy-failed"
		return res
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://agent/v1/backups/manager", bytes.NewReader(b))
	if err != nil {
		res.Error = "failed"
		return res
	}
	resp, err := m.agent.backups.Do(req)
	if err != nil {
		m.log.Error("backing up the manager's database", "err", err)
		res.Error = "agent-unreachable"
		return res
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&res); err != nil || resp.StatusCode != http.StatusOK {
		res.Snapshot, res.Error = "", "failed"
	}
	return res
}

// warnOnce raises a warning unless the source has it open.
func (m *Manager) warnOnce(ctx context.Context, source, what string) {
	if open, err := m.db.OpenWarning(ctx, source, what); err != nil || open {
		return
	}
	if err := m.db.AddWarning(ctx, store.Warning{At: m.cfg.Now(), Source: source, What: what}); err != nil {
		m.log.Error("raising a warning", "source", source, "err", err)
	}
}
