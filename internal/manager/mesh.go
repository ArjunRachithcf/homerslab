package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/manager/setup"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/mesh/netbird"
)

// NetBird is how the manager reaches NetBird's management API: the netbird package, or a test's stand-in.
type NetBird interface {
	Probe(ctx context.Context, url string) (setupRequired bool, err error)
	Bootstrap(ctx context.Context, url string, o netbird.Owner) (netbird.Bootstrapped, error)
	Adopt(ctx context.Context, url string, given mesh.Secret) (mesh.Token, error)
	Open(url string, t mesh.Token) (mesh.Mesh, error)
}

type realNetBird struct{}

func (realNetBird) Probe(ctx context.Context, url string) (bool, error) {
	return netbird.Probe(ctx, url)
}
func (realNetBird) Bootstrap(ctx context.Context, url string, o netbird.Owner) (netbird.Bootstrapped, error) {
	return netbird.Bootstrap(ctx, url, o)
}
func (realNetBird) Adopt(ctx context.Context, url string, given mesh.Secret) (mesh.Token, error) {
	return netbird.Adopt(ctx, url, given)
}
func (realNetBird) Open(url string, t mesh.Token) (mesh.Mesh, error) { return netbird.Open(url, t) }

// The manager's own credentials, sealed by the agent.
const (
	credMeshToken = "manager-mesh-token"
	credMeshOwner = "manager-mesh-owner-password"
)

// The network step's answers that the manager keeps. None is secret.
const (
	ansMesh         = "mesh"
	ansControlPlane = "meshControlPlane"
	ansURL          = "meshUrl"
	ansTokenUser    = "meshTokenUser"
	ansTokenID      = "meshTokenId"
	ansTokenExpires = "meshTokenExpires"
	ansPrimaryIP    = "primaryMeshIp"
)

// network is the LAN page's network step: the mesh and where its control plane is. It carries no secret. A new
// self-hosted NetBird is set up right here, in one step; otherwise the step waits for the token, which the admin
// gives once in this machine's terminal (`sudo homerslab setup secret mesh`, #64).
func (m *Manager) network(ctx context.Context, raw json.RawMessage, earlier map[string]string) (map[string]string, error) {
	var a struct {
		Mesh         string `json:"mesh"`
		ControlPlane string `json:"controlPlane"`
		URL          string `json:"url"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, &setup.Invalid{Fields: []string{"answer"}}
	}
	if a.Mesh != "netbird" {
		return nil, &setup.Invalid{Fields: []string{"mesh"}} // Tailscale and Headscale come with their own tickets
	}
	keep := map[string]string{ansMesh: a.Mesh, ansControlPlane: a.ControlPlane}
	switch a.ControlPlane {
	case "cloud":
		keep[ansURL] = netbird.CloudURL
		return nil, &setup.NeedsSecret{Secret: "mesh", Keep: keep}
	case "self-hosted":
	default:
		return nil, &setup.Invalid{Fields: []string{"controlPlane"}}
	}
	u, err := mesh.CheckURL(strings.TrimSpace(a.URL))
	if err != nil {
		return nil, &setup.Invalid{Fields: []string{"url"}}
	}
	keep[ansURL] = u.String()
	if earlier[ansURL] == u.String() && earlier[ansTokenID] != "" {
		// HomersLab has its own token here already (an earlier try stopped half-way): carry on with it.
		if t, err := m.currentToken(ctx, earlier); err == nil {
			return m.joinMesh(ctx, u.String(), t, keep)
		}
	}
	required, err := m.netbird.Probe(ctx, u.String())
	if err != nil {
		m.log.Warn("asking a NetBird server whether it is set up", "url", u.String(), "err", err)
		return nil, &setup.Invalid{Fields: []string{"url"}}
	}
	if !required {
		return nil, &setup.NeedsSecret{Secret: "mesh", Keep: keep}
	}
	b, err := m.netbird.Bootstrap(ctx, u.String(), netbird.Owner{Name: earlier["name"], Mail: earlier["mail"]})
	if errors.Is(err, mesh.ErrAlreadySetUp) {
		return nil, &setup.NeedsSecret{Secret: "mesh", Keep: keep}
	}
	if err != nil {
		return nil, fmt.Errorf("setting up the new NetBird: %w", err)
	}
	if err := m.agent.seal(ctx, credMeshOwner, b.OwnerPassword); err != nil {
		return nil, err
	}
	return m.joinMesh(ctx, u.String(), b.Token, keep)
}

// meshSecret takes the mesh token from the terminal: HomersLab makes its own service user and token with it, and
// then forgets it.
func (m *Manager) meshSecret(ctx context.Context, value string, earlier map[string]string) (map[string]string, error) {
	given := mesh.Secret(strings.TrimSpace(value))
	url := earlier[ansURL]
	t, err := m.netbird.Adopt(ctx, url, given)
	if err != nil {
		return nil, err
	}
	return m.joinMesh(ctx, url, t, map[string]string{ansMesh: earlier[ansMesh], ansControlPlane: earlier[ansControlPlane], ansURL: url})
}

var notLabel = regexp.MustCompile(`[^a-z0-9-]+`)

// joinMesh keeps HomersLab's own token sealed, makes the groups and policies per role (removing the mesh's default),
// and has the primary's agent join with a one-time key; the agent's firewall table then keeps the LAN out.
func (m *Manager) joinMesh(ctx context.Context, url string, t mesh.Token, keep map[string]string) (map[string]string, error) {
	if err := m.agent.seal(ctx, credMeshToken, t.Value); err != nil {
		return nil, err
	}
	keep[ansTokenUser], keep[ansTokenID], keep[ansTokenExpires] = t.UserID, t.ID, t.ExpiresAt.UTC().Format(time.RFC3339)
	// Kept before anything else can fail: the token is HomersLab's from here on, and the scheduler rotates it.
	if err := m.db.SaveAnswers(ctx, keep); err != nil {
		return nil, err
	}
	nb, err := m.netbird.Open(url, t)
	if err != nil {
		return nil, err
	}
	if err := nb.EnsurePolicies(ctx); err != nil {
		return nil, fmt.Errorf("the mesh's groups and policies: %w", err)
	}
	hostname := "primary"
	if f, err := m.agent.node(ctx); err == nil {
		if h := strings.Trim(notLabel.ReplaceAllString(strings.ToLower(f.Hostname), "-"), "-"); h != "" {
			hostname = h
		}
	}
	key, err := nb.NewJoinKey(ctx, hostname)
	if err != nil {
		return nil, fmt.Errorf("the primary's join key: %w", err)
	}
	joined, err := m.agent.join(ctx, agent.JoinRequest{Mesh: "netbird", ManagementURL: url, SetupKey: key.Value.Reveal(), Hostname: hostname})
	if err != nil {
		return nil, err
	}
	keep[ansPrimaryIP] = joined.IP.String()
	m.log.Info("the primary joined the mesh", "mesh", "netbird", "ip", joined.IP)
	return keep, nil
}

// putSecret is `homerslab setup secret <name>`, on the control socket only: the secret arrives from this machine's
// terminal, is checked, and finishes the step that waits for it. It is never logged, kept or answered.
func (m *Manager) putSecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil || strings.TrimSpace(body.Value) == "" {
		fail(w, http.StatusBadRequest, "no-value")
		return
	}
	st, err := m.setup.Secret(r.Context(), r.PathValue("name"), body.Value)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, st)
	case errors.Is(err, setup.ErrNotWaiting):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, mesh.ErrTokenRefused):
		fail(w, http.StatusBadRequest, "secret-refused")
	case errors.Is(err, mesh.ErrUnreachable):
		fail(w, http.StatusBadGateway, "mesh-unreachable")
	case errors.Is(err, errJoin):
		fail(w, http.StatusBadGateway, "join-failed")
	default:
		m.internal(w, "taking a secret", err)
	}
}

// rotate replaces HomersLab's mesh token before it expires: the service user mints its next one, the agent seals it,
// and the old one is revoked. It runs from the scheduler.
func (m *Manager) rotate(ctx context.Context) error {
	ans, err := m.db.Answers(ctx)
	if err != nil {
		return err
	}
	if ans[ansMesh] != "netbird" || ans[ansTokenExpires] == "" {
		return nil
	}
	expires, err := time.Parse(time.RFC3339, ans[ansTokenExpires])
	if err != nil {
		return err
	}
	if m.cfg.Now().Before(expires.Add(-mesh.RotateBefore)) {
		return nil
	}
	cur, err := m.currentToken(ctx, ans)
	if err != nil {
		return err
	}
	nb, err := m.netbird.Open(ans[ansURL], cur)
	if err != nil {
		return err
	}
	next, err := nb.Rotate(ctx)
	if err != nil {
		return fmt.Errorf("rotating the mesh token: %w", err)
	}
	if err := m.agent.seal(ctx, credMeshToken, next.Value); err != nil {
		return err // the old token still works; the next run tries again
	}
	if err := m.db.SaveAnswers(ctx, map[string]string{ansTokenID: next.ID, ansTokenUser: next.UserID,
		ansTokenExpires: next.ExpiresAt.UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	if err := nb.With(next).Revoke(ctx, cur); err != nil {
		m.log.Warn("revoking the old mesh token", "err", err) // it expires by itself; never its body
	}
	m.log.Info("rotated the mesh token", "expires", next.ExpiresAt)
	return nil
}

// currentToken is HomersLab's mesh token: its value from the agent, the rest from the answers.
func (m *Manager) currentToken(ctx context.Context, ans map[string]string) (mesh.Token, error) {
	expires, err := time.Parse(time.RFC3339, ans[ansTokenExpires])
	if err != nil {
		return mesh.Token{}, err
	}
	value, err := m.agent.unseal(ctx, credMeshToken)
	if err != nil {
		return mesh.Token{}, err
	}
	return mesh.Token{Value: value, UserID: ans[ansTokenUser], ID: ans[ansTokenID], ExpiresAt: expires}, nil
}

// RunDue runs the scheduler's jobs that are due: so far, the mesh token's rotation.
func (m *Manager) RunDue(ctx context.Context) {
	if err := m.rotate(ctx); err != nil {
		m.log.Error("the mesh token's rotation", "err", err)
	}
}

func (m *Manager) schedule(ctx context.Context) {
	t := time.NewTicker(m.cfg.ScheduleEvery)
	defer t.Stop()
	m.RunDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.RunDue(ctx)
		}
	}
}

// CloseLANListener closes the setup link's LAN listener for good: the firewall table drops its exception, and the
// setup listener answers nothing from then on. The admin's first sign-in over the mesh calls it (#17).
func (m *Manager) CloseLANListener(ctx context.Context) error {
	if err := m.agent.closeSetupListener(ctx); err != nil {
		return err
	}
	if err := m.db.SaveAnswers(ctx, map[string]string{ansLANClosed: m.cfg.Now().UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	m.lanClosed.Store(true)
	m.log.Info("the setup link's LAN listener is closed, for good")
	return nil
}

const ansLANClosed = "lanClosedAt"
