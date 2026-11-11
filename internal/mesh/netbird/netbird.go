// Package netbird is the mesh contract's NetBird adapter, cloud or self-hosted: its management API as HomersLab's own
// service user (this file), and NetBird's client on a node (node.go). Every call is from the NetBird spike
// (docs/research/2026-11-09-spike-netbird-api.md), and the contract suite runs it against a real self-hosted NetBird.
//
// NetBird's error bodies can carry a hash of the token that was refused, so this package never keeps or logs one:
// an error says the call and the status only.
package netbird

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// CloudURL is NetBird's cloud.
const CloudURL = "https://api.netbird.io"

// ServiceUser is the name of HomersLab's own user on a NetBird account.
const ServiceUser = "homerslab"

// tokenDays is the life HomersLab asks for its own token: NetBird's maximum, and mesh.MaxTokenLife.
const tokenDays = 365

// APIError is a call the management API refused. It holds no part of the answer's body.
type APIError struct {
	Method, Path string
	Status       int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("NetBird answered %s %s with %d", e.Method, e.Path, e.Status)
}

// api calls one management API with one token.
type api struct {
	base  *url.URL
	token mesh.Secret
	http  *http.Client
	now   func() time.Time
}

func newAPI(rawURL string, token mesh.Secret) (*api, error) {
	u, err := mesh.CheckURL(rawURL)
	if err != nil {
		return nil, err
	}
	return &api{base: u, token: token, http: &http.Client{Timeout: 20 * time.Second}, now: time.Now}, nil
}

// call sends body as JSON and decodes the answer into out. A refused token is mesh.ErrTokenRefused; anything that
// never got an answer is mesh.ErrUnreachable.
func (a *api) call(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	p, query, _ := strings.Cut(path, "?")
	u := a.base.JoinPath(p)
	u.RawQuery = query
	req, err := http.NewRequestWithContext(ctx, method, u.String(), r)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Token "+a.token.Reveal())
	}
	resp, err := a.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s", mesh.ErrUnreachable, a.base.Host)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%w: %s", mesh.ErrUnreachable, a.base.Host)
	}
	if resp.StatusCode/100 != 2 {
		// A revoked or unknown token gets 404 "invalid token", not 401 (the spike).
		if a.token != "" && (resp.StatusCode == http.StatusUnauthorized ||
			(resp.StatusCode == http.StatusNotFound && bytes.Contains(data, []byte("invalid token")))) {
			return mesh.ErrTokenRefused
		}
		return &APIError{Method: method, Path: path, Status: resp.StatusCode}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%w: %s %s answered something that isn't NetBird's API", mesh.ErrNotAMesh, method, path)
		}
	}
	return nil
}

// Mesh is a NetBird account, as HomersLab's service user.
type Mesh struct {
	api   *api
	token mesh.Token
}

var _ mesh.Mesh = (*Mesh)(nil)

// Open is the account at rawURL with HomersLab's own token.
func Open(rawURL string, t mesh.Token) (*Mesh, error) {
	a, err := newAPI(rawURL, t.Value)
	if err != nil {
		return nil, err
	}
	return &Mesh{api: a, token: t}, nil
}

// Probe says whether a self-hosted NetBird at rawURL waits for its first setup, which HomersLab can then do itself.
func Probe(ctx context.Context, rawURL string) (setupRequired bool, err error) {
	a, err := newAPI(rawURL, "")
	if err != nil {
		return false, err
	}
	var inst struct {
		SetupRequired *bool `json:"setup_required"`
	}
	if err := a.call(ctx, http.MethodGet, "/api/instance", nil, &inst); err != nil {
		var ae *APIError
		if errors.As(err, &ae) {
			return false, fmt.Errorf("%w (%d)", mesh.ErrNotAMesh, ae.Status)
		}
		return false, err
	}
	if inst.SetupRequired == nil {
		return false, mesh.ErrNotAMesh
	}
	return *inst.SetupRequired, nil
}

// Owner is the person a new self-hosted NetBird is set up for: the household admin. Its password is generated, and
// is the way back in at NetBird's own dashboard if every token is lost.
type Owner struct {
	Name, Mail string
}

// Bootstrapped is what HomersLab keeps from setting up a new self-hosted NetBird: its own token, and the owner's
// generated password (for the recovery kit).
type Bootstrapped struct {
	Token         mesh.Token
	OwnerPassword mesh.Secret
}

// Bootstrap sets up a new self-hosted NetBird in one step: its owner (with a generated password and a setup token
// that lasts a day), HomersLab's service user and its 365-day token, and then it revokes the setup token. It is
// refused with mesh.ErrAlreadySetUp when the server was set up before.
func Bootstrap(ctx context.Context, rawURL string, o Owner) (Bootstrapped, error) {
	return bootstrap(ctx, rawURL, o, func(mesh.Secret) {})
}

func bootstrap(ctx context.Context, rawURL string, o Owner, seen func(mesh.Secret)) (Bootstrapped, error) {
	required, err := Probe(ctx, rawURL)
	if err != nil {
		return Bootstrapped{}, err
	}
	if !required {
		return Bootstrapped{}, mesh.ErrAlreadySetUp
	}
	a, err := newAPI(rawURL, "")
	if err != nil {
		return Bootstrapped{}, err
	}
	password := newPassword()
	var setup struct {
		UserID string `json:"user_id"`
		PAT    string `json:"personal_access_token"`
	}
	err = a.call(ctx, http.MethodPost, "/api/setup", map[string]any{
		"email": o.Mail, "name": o.Name, "password": password.Reveal(), "create_pat": true, "pat_expire_in": 1,
	}, &setup)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusPreconditionFailed {
		return Bootstrapped{}, mesh.ErrAlreadySetUp // someone else was quicker
	}
	if err != nil {
		return Bootstrapped{}, err
	}
	if setup.PAT == "" {
		return Bootstrapped{}, errors.New("NetBird's setup made no token: is NB_SETUP_PAT_ENABLED=true set on the server?")
	}
	setupToken := mesh.Secret(setup.PAT)
	seen(setupToken)
	t, err := Adopt(ctx, rawURL, setupToken)
	if err != nil {
		return Bootstrapped{}, err
	}
	// The setup token has done its work; HomersLab's own one takes over.
	owner := &api{base: a.base, token: setupToken, http: a.http, now: a.now}
	var pats []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := owner.call(ctx, http.MethodGet, "/api/users/"+url.PathEscape(setup.UserID)+"/tokens", nil, &pats); err != nil {
		return Bootstrapped{}, err
	}
	for _, p := range pats {
		if err := owner.call(ctx, http.MethodDelete, "/api/users/"+url.PathEscape(setup.UserID)+"/tokens/"+url.PathEscape(p.ID), nil, nil); err != nil {
			return Bootstrapped{}, err
		}
	}
	return Bootstrapped{Token: t, OwnerPassword: password}, nil
}

// Adopt takes the token the admin gave once and makes HomersLab's own: its service user (made if missing) and that
// user's new 365-day token. HomersLab's older tokens are revoked. The given token is never kept.
func Adopt(ctx context.Context, rawURL string, given mesh.Secret) (mesh.Token, error) {
	a, err := newAPI(rawURL, given)
	if err != nil {
		return mesh.Token{}, err
	}
	var users []user
	if err := a.call(ctx, http.MethodGet, "/api/users?service_user=true", nil, &users); err != nil {
		return mesh.Token{}, err
	}
	var svc *user
	for i := range users {
		if users[i].IsServiceUser && users[i].Name == ServiceUser {
			svc = &users[i]
		}
	}
	if svc == nil {
		var made user
		if err := a.call(ctx, http.MethodPost, "/api/users", map[string]any{
			"name": ServiceUser, "role": "admin", "auto_groups": []string{}, "is_service_user": true,
		}, &made); err != nil {
			return mesh.Token{}, err
		}
		svc = &made
	}
	t, err := a.newToken(ctx, svc.ID)
	if err != nil {
		return mesh.Token{}, err
	}
	// From here on HomersLab speaks for itself.
	own := &api{base: a.base, token: t.Value, http: a.http, now: a.now}
	tokens, err := own.tokens(ctx, svc.ID)
	if err != nil {
		return mesh.Token{}, err
	}
	for _, old := range tokens {
		if old.ID != t.ID {
			if err := own.call(ctx, http.MethodDelete, "/api/users/"+url.PathEscape(svc.ID)+"/tokens/"+url.PathEscape(old.ID), nil, nil); err != nil {
				return mesh.Token{}, err
			}
		}
	}
	return t, nil
}

type user struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	IsServiceUser bool   `json:"is_service_user"`
}

type pat struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Expiration time.Time `json:"expiration_date"`
}

func (a *api) tokens(ctx context.Context, userID string) ([]pat, error) {
	var out []pat
	return out, a.call(ctx, http.MethodGet, "/api/users/"+url.PathEscape(userID)+"/tokens", nil, &out)
}

// newToken mints a token for userID that lives tokenDays, and refuses one that would live longer.
func (a *api) newToken(ctx context.Context, userID string) (mesh.Token, error) {
	var made struct {
		Plain string `json:"plain_token"`
		PAT   pat    `json:"personal_access_token"`
	}
	name := "homerslab-" + a.now().UTC().Format("20060102-150405")
	if err := a.call(ctx, http.MethodPost, "/api/users/"+url.PathEscape(userID)+"/tokens", map[string]any{
		"name": name, "expires_in": tokenDays,
	}, &made); err != nil {
		return mesh.Token{}, err
	}
	if made.Plain == "" || made.PAT.ID == "" {
		return mesh.Token{}, errors.New("NetBird made a token without a value")
	}
	t := mesh.Token{Value: mesh.Secret(made.Plain), UserID: userID, ID: made.PAT.ID, ExpiresAt: made.PAT.Expiration}
	if t.ExpiresAt.IsZero() || t.ExpiresAt.After(a.now().Add(mesh.MaxTokenLife+time.Hour)) {
		_ = a.call(ctx, http.MethodDelete, "/api/users/"+url.PathEscape(userID)+"/tokens/"+url.PathEscape(t.ID), nil, nil)
		return mesh.Token{}, fmt.Errorf("NetBird gave a token that expires at %v, beyond %d days", t.ExpiresAt, tokenDays)
	}
	return t, nil
}

// Token is the token this Mesh uses.
func (m *Mesh) Token() mesh.Token { return m.token }

// With is this account with another of HomersLab's tokens.
func (m *Mesh) With(t mesh.Token) mesh.Mesh {
	return &Mesh{api: &api{base: m.api.base, token: t.Value, http: m.api.http, now: m.api.now}, token: t}
}

// Rotate mints HomersLab's next token as the service user itself. The caller keeps it, then revokes the old one.
func (m *Mesh) Rotate(ctx context.Context) (mesh.Token, error) {
	return m.api.newToken(ctx, m.token.UserID)
}

// Revoke revokes one of HomersLab's own tokens.
func (m *Mesh) Revoke(ctx context.Context, t mesh.Token) error {
	return m.api.call(ctx, http.MethodDelete, "/api/users/"+url.PathEscape(t.UserID)+"/tokens/"+url.PathEscape(t.ID), nil, nil)
}

// Report says what this NetBird can do.
func (m *Mesh) Report(ctx context.Context) (mesh.Report, error) {
	r := mesh.Report{Mesh: "netbird", ControlPlane: "self-hosted", PerAppNames: "wildcard-records",
		PublicPath: "netbird-reverse-proxy", DomainRequired: true, PortMappingOff: true}
	if m.api.base.Host == "api.netbird.io" {
		r.ControlPlane = "cloud"
	}
	var v struct {
		Current string `json:"management_current_version"`
	}
	if err := m.api.call(ctx, http.MethodGet, "/api/instance/version", nil, &v); err != nil {
		var ae *APIError
		if !errors.As(err, &ae) { // the cloud may not say; a refused token or no answer is an error
			return r, err
		}
	}
	r.Version = v.Current
	var clusters []struct {
		Online bool `json:"online"`
	}
	if err := m.api.call(ctx, http.MethodGet, "/api/reverse-proxies/clusters", nil, &clusters); err != nil {
		var ae *APIError
		if !errors.As(err, &ae) {
			return r, err
		}
	}
	r.PublicPathReady = slices.ContainsFunc(clusters, func(c struct {
		Online bool `json:"online"`
	}) bool {
		return c.Online
	})
	return r, nil
}

type group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// groups makes every group HomersLab needs and returns all of them by name.
func (m *Mesh) groups(ctx context.Context) (map[string]string, error) {
	var list []group
	if err := m.api.call(ctx, http.MethodGet, "/api/groups", nil, &list); err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, g := range list {
		ids[g.Name] = g.ID
	}
	want := []string{mesh.NodesGroup}
	for _, r := range mesh.Roles {
		want = append(want, mesh.RoleGroup(r))
	}
	for _, name := range want {
		if ids[name] != "" {
			continue
		}
		var g group
		if err := m.api.call(ctx, http.MethodPost, "/api/groups", map[string]any{"name": name}, &g); err != nil {
			return nil, err
		}
		ids[name] = g.ID
	}
	return ids, nil
}

type policy struct {
	ID          string       `json:"id,omitempty"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Enabled     bool         `json:"enabled"`
	Rules       []policyRule `json:"rules"`
}

type policyRule struct {
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	Enabled       bool             `json:"enabled"`
	Action        string           `json:"action"`
	Bidirectional bool             `json:"bidirectional"`
	Protocol      string           `json:"protocol"`
	Ports         []string         `json:"ports"`
	Sources       groupRefs        `json:"sources"`
	Destinations  groupRefs        `json:"destinations"`
	SourceRes     *json.RawMessage `json:"sourceResource,omitempty"`
	DestRes       *json.RawMessage `json:"destinationResource,omitempty"`
}

// groupRefs are a rule's groups: NetBird takes their IDs and answers with objects.
type groupRefs []string

func (g *groupRefs) UnmarshalJSON(b []byte) error {
	var ids []string
	if json.Unmarshal(b, &ids) == nil {
		*g = ids
		return nil
	}
	var objs []group
	if err := json.Unmarshal(b, &objs); err != nil {
		return err
	}
	*g = make(groupRefs, len(objs))
	for i, o := range objs {
		(*g)[i] = o.ID
	}
	return nil
}

// desired is a Rule as NetBird's policy.
func desired(r mesh.Rule, ids map[string]string) policy {
	ports := make([]string, len(r.Ports))
	for i, p := range r.Ports {
		ports[i] = strconv.Itoa(p)
	}
	return policy{Name: r.Name, Description: "Made by HomersLab; it puts it back if changed.", Enabled: true, Rules: []policyRule{{
		Name: r.Name, Description: "Made by HomersLab.", Enabled: true, Action: "accept", Protocol: "tcp", Ports: ports,
		Sources: groupRefs{ids[r.From]}, Destinations: groupRefs{ids[r.To]},
	}}}
}

// same says whether a policy on the account does what want does, and nothing more.
func same(have, want policy) bool {
	if !have.Enabled || len(have.Rules) != 1 {
		return false
	}
	h, w := have.Rules[0], want.Rules[0]
	return h.Enabled && h.Action == w.Action && h.Protocol == w.Protocol && h.Bidirectional == w.Bidirectional &&
		sortedEqual(h.Ports, w.Ports) && sortedEqual(h.Sources, w.Sources) && sortedEqual(h.Destinations, w.Destinations) &&
		h.SourceRes == nil && h.DestRes == nil
}

func sortedEqual(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// EnsurePolicies makes HomersLab's groups and policies, puts back any that drifted, and removes NetBird's Default
// policy ("everyone reaches everything"). The household's own policies and groups stay as they are.
func (m *Mesh) EnsurePolicies(ctx context.Context) error {
	ids, err := m.groups(ctx)
	if err != nil {
		return err
	}
	var have []policy
	if err := m.api.call(ctx, http.MethodGet, "/api/policies", nil, &have); err != nil {
		return err
	}
	byName := map[string]policy{}
	for _, p := range have {
		byName[p.Name] = p
	}
	for _, r := range mesh.Policies() {
		want := desired(r, ids)
		p, ok := byName[r.Name]
		switch {
		case !ok:
			err = m.api.call(ctx, http.MethodPost, "/api/policies", want, nil)
		case !same(p, want):
			err = m.api.call(ctx, http.MethodPut, "/api/policies/"+url.PathEscape(p.ID), want, nil)
		}
		if err != nil {
			return err
		}
	}
	for _, p := range have {
		if p.Name == "Default" {
			if err := m.api.call(ctx, http.MethodDelete, "/api/policies/"+url.PathEscape(p.ID), nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// NewJoinKey makes a one-off setup key that puts one node in the nodes group, and expires in mesh.JoinKeyLife.
func (m *Mesh) NewJoinKey(ctx context.Context, node string) (mesh.JoinKey, error) {
	ids, err := m.groups(ctx)
	if err != nil {
		return mesh.JoinKey{}, err
	}
	var k struct {
		ID      string    `json:"id"`
		Key     string    `json:"key"`
		Expires time.Time `json:"expires"`
	}
	if err := m.api.call(ctx, http.MethodPost, "/api/setup-keys", map[string]any{
		"name": "homerslab-node-" + node, "type": "one-off", "expires_in": int(mesh.JoinKeyLife / time.Second),
		"auto_groups": []string{ids[mesh.NodesGroup]}, "usage_limit": 1, "ephemeral": false,
	}, &k); err != nil {
		return mesh.JoinKey{}, err
	}
	if k.Key == "" {
		return mesh.JoinKey{}, errors.New("NetBird made a setup key without a value")
	}
	return mesh.JoinKey{Value: mesh.Secret(k.Key), ID: k.ID, ExpiresAt: k.Expires}, nil
}

// newPassword is a password NetBird's embedded sign-in takes: 32 random characters, with every class it may ask for.
func newPassword() mesh.Secret {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return mesh.Secret(base64.RawURLEncoding.EncodeToString(b) + "-Aa1")
}
