package agent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/host"
)

// The sign-in on the primary: the household account's identity provider, Authentik, as one systemd unit the package
// ships (homerslab-authentik.service), which runs the standard compose command on the package's compose file
// (packaging/sign-in). The agent makes Authentik's own secrets, seals them for that unit (ADR 0011), and writes the
// unit's drop-in that loads them; the manager never sees them. The manager gives only the bootstrap token, which it
// uses once and then has the agent forget.
const (
	SignInUnit = "homerslab-authentik.service"
	// SignInUpstream is the server's fixed address on the sign-in network, as the package's compose file pins it:
	// where the edge, and the manager, reach it.
	SignInUpstream = "172.31.250.10:9000"
	// signInLevel is the sign-in's own SELinux MCS level, which its containers run at (the compose file).
	signInLevel = "s0:c1000,c1001"
	// signInRun is where the unit puts the files its containers mount: the unit's RuntimeDirectory, 0700.
	signInRun = "/run/homerslab-authentik"
	// The container users that read the secrets: Authentik's image runs as 1000, PostgreSQL's (alpine) as 70.
	authentikUID = "1000"
	postgresUID  = "70"
)

// The sign-in's credentials, by the IDs its unit loads them as.
const (
	credSecretKey = "authentik.secret-key"
	credPGPass    = "authentik.postgres-password"
	credBootstrap = "authentik.bootstrap-token"
)

// SignInRequest is the sign-in as the manager wants it. BootstrapToken travels inward only.
type SignInRequest struct {
	Provider       string `json:"provider"`
	BootstrapToken string `json:"bootstrapToken,omitempty"`
}

// SignInStatus is the sign-in as the agent finds it. None of it is secret.
type SignInStatus struct {
	Provider string `json:"provider"`
	Unit     string `json:"unit"`
	// Upstream is where the edge and the manager reach the provider's server.
	Upstream string `json:"upstream"`
	// Active: systemd runs the unit. Ready: the provider's own readiness check passes.
	Active bool `json:"active"`
	Ready  bool `json:"ready"`
	// Bootstrapping: the unit still hands Authentik its bootstrap token.
	Bootstrapping bool `json:"bootstrapping"`
}

// signInState is what the agent keeps of the sign-in, in agent.json.
type signInState struct {
	Provider      string `json:"provider"`
	Bootstrapping bool   `json:"bootstrapping"`
}

func (a *Agent) signInDir() string { return filepath.Join(a.cfg.StateDir, "sign-in") }

func (a *Agent) signInCred(id string) string { return filepath.Join(a.signInDir(), id+".cred") }

func (a *Agent) signInDropIn() string {
	return filepath.Join("etc/systemd/system", SignInUnit+".d", "50-homerslab-credentials.conf")
}

func (a *Agent) upstream() string {
	if a.cfg.SignInUpstream != "" {
		return a.cfg.SignInUpstream
	}
	return SignInUpstream
}

// selinux says whether the machine's kernel enforces SELinux, or could: then the unit labels the secrets for the
// sign-in's containers.
func (a *Agent) selinux() bool {
	if a.cfg.Sysroot == nil {
		return false
	}
	_, err := fs.Stat(a.cfg.Sysroot, "sys/fs/selinux/enforce")
	return err == nil
}

// signInCredentials is the sign-in unit's drop-in: each credential sealed by the agent, decrypted by systemd as the
// unit starts, copied for the one container user that reads it, and labelled at the sign-in's level. While there is
// no bootstrap token the file the server reads it from is empty.
func (a *Agent) signInCredentials(bootstrapping bool) []byte {
	var b strings.Builder
	b.WriteString("# Written by HomersLab's agent: the sign-in's secrets, sealed by the agent (ADR 0011). Never edit by hand.\n[Service]\n")
	ids := []string{credSecretKey, credPGPass}
	if bootstrapping {
		ids = append(ids, credBootstrap)
	}
	for _, id := range ids {
		fmt.Fprintf(&b, "LoadCredentialEncrypted=%s:%s\n", id, a.signInCred(id))
	}
	install := func(owner, from, to string) {
		fmt.Fprintf(&b, "ExecStartPre=/usr/bin/install -m 0400 -o %s -g %s %s %s/%s\n", owner, owner, from, signInRun, to)
	}
	install(authentikUID, "%d/"+credSecretKey, "secret-key")
	install(authentikUID, "%d/"+credPGPass, "postgres-password")
	install(postgresUID, "%d/"+credPGPass, "postgres-password-db")
	if bootstrapping {
		install(authentikUID, "%d/"+credBootstrap, "bootstrap-token")
	} else {
		install(authentikUID, "/dev/null", "bootstrap-token")
	}
	if a.selinux() {
		files := []string{"secret-key", "postgres-password", "postgres-password-db", "bootstrap-token"}
		for i, f := range files {
			files[i] = signInRun + "/" + f
		}
		fmt.Fprintf(&b, "ExecStartPre=/usr/bin/chcon -t container_file_t -l %s %s\n", signInLevel, strings.Join(files, " "))
	}
	return []byte(b.String())
}

// newSecret is 384 random bits, for Authentik's secret key and its database's password.
func newSecret() []byte {
	b := make([]byte, 48)
	_, _ = rand.Read(b)
	return []byte(base64.RawURLEncoding.EncodeToString(b))
}

// putSignIn installs the sign-in, or brings it in line: its secrets made once and sealed, the bootstrap token sealed
// when the manager gives one, the unit's drop-in, then a restart, since systemd hands credentials over only as a unit
// starts.
func (a *Agent) putSignIn(w http.ResponseWriter, r *http.Request) {
	var req SignInRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	if req.Provider != "authentik" || strings.ContainsAny(req.BootstrapToken, " \t\r\n\x00") || len(req.BootstrapToken) > 512 {
		fail(w, http.StatusBadRequest, "bad-sign-in") // Kanidm comes with its own ticket
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx := r.Context()
	if err := os.MkdirAll(a.signInDir(), 0o700); err != nil {
		a.log.Error("the sign-in's credentials", "err", err)
		fail(w, http.StatusInternalServerError, "seal-failed")
		return
	}
	for _, id := range []string{credSecretKey, credPGPass} {
		if _, err := os.Stat(a.signInCred(id)); err == nil {
			continue // made once: Authentik's data opens only with them
		}
		if err := a.sealAt(ctx, a.signInCred(id), id, newSecret()); err != nil {
			a.log.Error("sealing the sign-in's secret", "id", id, "err", err)
			fail(w, http.StatusInternalServerError, "seal-failed")
			return
		}
	}
	next := signInState{Provider: req.Provider}
	if a.state.SignIn != nil {
		next.Bootstrapping = a.state.SignIn.Bootstrapping
	}
	if req.BootstrapToken != "" {
		if err := a.sealAt(ctx, a.signInCred(credBootstrap), credBootstrap, []byte(req.BootstrapToken)); err != nil {
			a.log.Error("sealing the sign-in's bootstrap token", "err", err)
			fail(w, http.StatusInternalServerError, "seal-failed")
			return
		}
		next.Bootstrapping = true
	}
	if err := a.applySignIn(ctx, next); err != nil {
		a.log.Error("starting the sign-in", "err", err)
		fail(w, http.StatusInternalServerError, "sign-in-failed")
		return
	}
	a.log.Info("the sign-in's unit runs with its sealed secrets", "provider", next.Provider, "bootstrapping", next.Bootstrapping)
	writeJSON(w, http.StatusOK, a.signInStatusLocked())
}

// applySignIn keeps the sign-in's state, writes its drop-in, and restarts its unit. The caller holds a.mu.
func (a *Agent) applySignIn(ctx context.Context, next signInState) error {
	prev := a.state.SignIn
	a.state.SignIn = &next
	if err := a.save(); err != nil {
		a.state.SignIn = prev
		return err
	}
	for _, step := range []func() error{
		func() error { return a.writeAt(a.signInDropIn(), a.signInCredentials(next.Bootstrapping)) },
		func() error { return a.run(ctx, "systemctl", "daemon-reload") },
		func() error { return a.run(ctx, "systemctl", "enable", SignInUnit) },
		func() error { return a.run(ctx, "systemctl", "restart", SignInUnit) },
	} {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// deleteBootstrapToken forgets the bootstrap token once the manager has its own, and restarts the unit without it.
func (a *Agent) deleteBootstrapToken(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.SignIn == nil {
		fail(w, http.StatusNotFound, "no-sign-in")
		return
	}
	next := *a.state.SignIn
	next.Bootstrapping = false
	if err := a.applySignIn(r.Context(), next); err != nil {
		a.log.Error("restarting the sign-in without its bootstrap token", "err", err)
		fail(w, http.StatusInternalServerError, "sign-in-failed")
		return
	}
	if err := os.Remove(a.signInCred(credBootstrap)); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.log.Error("removing the bootstrap token", "err", err)
		fail(w, http.StatusInternalServerError, "remove-failed")
		return
	}
	a.log.Info("the sign-in's bootstrap token is gone")
	w.WriteHeader(http.StatusNoContent)
}

func (a *Agent) signInStatusLocked() SignInStatus {
	s := SignInStatus{Unit: SignInUnit, Upstream: "http://" + a.upstream()}
	if a.state.SignIn != nil {
		s.Provider, s.Bootstrapping = a.state.SignIn.Provider, a.state.SignIn.Bootstrapping
	}
	return s
}

// getSignIn is the sign-in's state: systemd's for the unit, and the provider's own readiness check, each for hostWait
// at most and outside the agent's lock.
func (a *Agent) getSignIn(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	installed := a.state.SignIn != nil
	s := a.signInStatusLocked()
	a.mu.Unlock()
	if !installed {
		fail(w, http.StatusNotFound, "no-sign-in")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), hostWait)
	defer cancel()
	_, err := a.cfg.Run(ctx, host.Cmd{Name: "systemctl", Args: []string{"is-active", "--quiet", SignInUnit}})
	s.Active = err == nil
	if s.Active {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.Upstream+"/-/health/ready/", nil)
		if resp, err := (&http.Client{Timeout: hostWait}).Do(req); err == nil {
			resp.Body.Close()
			s.Ready = resp.StatusCode/100 == 2
		}
	}
	writeJSON(w, http.StatusOK, s)
}

// getSignInLogs is the unit's last lines in the journal. Authentik logs no secret; the unit's own lines name only
// files.
func (a *Agent) getSignInLogs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), hostWait)
	defer cancel()
	out, err := a.cfg.Run(ctx, host.Cmd{Name: "journalctl", Args: []string{"--unit=" + SignInUnit, "--lines=200", "--no-pager", "--output=short-iso"}})
	if err != nil {
		a.log.Warn("reading the sign-in's journal", "err", err)
		fail(w, http.StatusBadGateway, "journal-unreadable")
		return
	}
	if len(out) > 256<<10 {
		out = out[len(out)-256<<10:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"unit": SignInUnit, "lines": strings.Split(strings.TrimRight(string(out), "\n"), "\n"),
		"at": time.Now().UTC()})
}
