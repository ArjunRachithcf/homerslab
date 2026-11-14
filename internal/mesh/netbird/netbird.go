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
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// CloudURL is NetBird's cloud.
const CloudURL = "https://api.netbird.io"

// CloudPort is the household's port on NetBird's cloud (ADR 0021): the TLS port its shared clusters accept for TLS
// passthrough services. Classic found 443 and 8443 refused and 9443 working on the eu1 cluster (2026-10-17); the
// manual release checklist tries it again (docs/manual-checks.md).
const CloudPort = 9443

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
	return &api{base: u, token: token, now: time.Now, http: &http.Client{
		Timeout: 20 * time.Second,
		// A token goes to the address the admin gave, and nowhere a redirect points: a redirect is an answer.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
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
	if resp.StatusCode/100 == 3 {
		return fmt.Errorf("%w: %s %s answered with a redirect, which HomersLab doesn't follow", mesh.ErrNotAMesh, method, path)
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

// Owner is the person a new self-hosted NetBird is set up for: the household admin. Password is the way back in at
// NetBird's own dashboard if every token is lost; the caller makes it (NewPassword) and keeps it before Bootstrap
// runs, so it is never lost half-way.
type Owner struct {
	Name, Mail string
	Password   mesh.Secret
}

// SetupToken is the owner's token from a new self-hosted NetBird's first setup: it lasts a day. The caller seals it
// before anything else happens, so a setup that stops half-way resumes from it, and revokes it (RevokeSetup) once
// HomersLab's own token works.
type SetupToken struct {
	Value  mesh.Secret
	UserID string
}

// Setup runs a new self-hosted NetBird's first setup: its owner, with a setup token that lasts a day. It is refused
// with mesh.ErrAlreadySetUp when the server was set up before.
func Setup(ctx context.Context, rawURL string, o Owner) (SetupToken, error) {
	if len(o.Password.Reveal()) < 16 {
		return SetupToken{}, errors.New("the owner's password is too short")
	}
	required, err := Probe(ctx, rawURL)
	if err != nil {
		return SetupToken{}, err
	}
	if !required {
		return SetupToken{}, mesh.ErrAlreadySetUp
	}
	a, err := newAPI(rawURL, "")
	if err != nil {
		return SetupToken{}, err
	}
	var setup struct {
		UserID string `json:"user_id"`
		PAT    string `json:"personal_access_token"`
	}
	err = a.call(ctx, http.MethodPost, "/api/setup", map[string]any{
		"email": o.Mail, "name": o.Name, "password": o.Password.Reveal(), "create_pat": true, "pat_expire_in": 1,
	}, &setup)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusPreconditionFailed {
		return SetupToken{}, mesh.ErrAlreadySetUp // someone else was quicker
	}
	if err != nil {
		return SetupToken{}, err
	}
	if setup.PAT == "" || setup.UserID == "" {
		return SetupToken{}, errors.New("NetBird's setup made no token: is NB_SETUP_PAT_ENABLED=true set on the server?")
	}
	return SetupToken{Value: mesh.Secret(setup.PAT), UserID: setup.UserID}, nil
}

// RevokeSetup revokes the owner's tokens with the setup token itself: HomersLab's own token has taken over.
func RevokeSetup(ctx context.Context, rawURL string, st SetupToken) error {
	owner, err := newAPI(rawURL, st.Value)
	if err != nil {
		return err
	}
	pats, err := owner.tokens(ctx, st.UserID)
	if err != nil {
		return err
	}
	for _, p := range pats {
		if err := owner.call(ctx, http.MethodDelete, "/api/users/"+url.PathEscape(st.UserID)+"/tokens/"+url.PathEscape(p.ID), nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// Adopted is HomersLab's new token, and its older ones: the caller revokes those (Mesh.Revoke) only once the new
// token is sealed and has worked.
type Adopted struct {
	Token mesh.Token
	Stale []mesh.Token
}

// Adopt takes the token the admin gave once (or a new NetBird's setup token) and makes HomersLab's own: its service
// user (made if missing) and that user's new 365-day token. The given token is never kept.
func Adopt(ctx context.Context, rawURL string, given mesh.Secret) (Adopted, error) {
	a, err := newAPI(rawURL, given)
	if err != nil {
		return Adopted{}, err
	}
	var users []user
	if err := a.call(ctx, http.MethodGet, "/api/users?service_user=true", nil, &users); err != nil {
		return Adopted{}, err
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
			return Adopted{}, err
		}
		svc = &made
	}
	t, err := a.newToken(ctx, svc.ID)
	if err != nil {
		return Adopted{}, err
	}
	own := &api{base: a.base, token: t.Value, http: a.http, now: a.now}
	tokens, err := own.tokens(ctx, svc.ID)
	if err != nil {
		return Adopted{}, err
	}
	out := Adopted{Token: t}
	for _, old := range tokens {
		if old.ID != t.ID {
			out.Stale = append(out.Stale, mesh.Token{UserID: svc.ID, ID: old.ID, ExpiresAt: old.Expiration})
		}
	}
	return out, nil
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
		PublicPath: "netbird-reverse-proxy", DomainRequired: true, Port: 443} // PortMappingOff is the nodes' to say: their agents report it
	if m.api.base.Host == "api.netbird.io" {
		r.ControlPlane, r.Port = "cloud", CloudPort
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
	// SourcePostureChecks are the checks a device must pass first; HomersLab's policies have none.
	SourcePostureChecks []string `json:"source_posture_checks"`
}

type policyRule struct {
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Enabled       bool      `json:"enabled"`
	Action        string    `json:"action"`
	Bidirectional bool      `json:"bidirectional"`
	Protocol      string    `json:"protocol"`
	Ports         []string  `json:"ports"`
	Sources       groupRefs `json:"sources"`
	Destinations  groupRefs `json:"destinations"`
	SourceRes     *resource `json:"sourceResource,omitempty"`
	DestRes       *resource `json:"destinationResource,omitempty"`
	// PortRanges and AuthorizedGroups widen or change what a rule lets through; HomersLab's rules have none.
	PortRanges       []json.RawMessage `json:"port_ranges"`
	AuthorizedGroups json.RawMessage   `json:"authorized_groups,omitempty"`
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
	return policy{Name: r.Name, Description: "Made by HomersLab; it puts it back if changed.", Enabled: true,
		SourcePostureChecks: []string{}, Rules: []policyRule{{
			Name: r.Name, Description: "Made by HomersLab.", Enabled: true, Action: "accept", Protocol: "tcp", Ports: ports,
			Sources: groupRefs{ids[r.From]}, Destinations: groupRefs{ids[r.To]}, PortRanges: []json.RawMessage{},
		}}}
}

// same says whether a policy on the account does what want does, and nothing more: its whole rule as NetBird
// describes it, port ranges and posture checks included.
func same(have, want policy) bool {
	if !have.Enabled || len(have.Rules) != 1 || len(have.SourcePostureChecks) != 0 {
		return false
	}
	if r := have.Rules[0]; len(r.PortRanges) != 0 || (len(r.AuthorizedGroups) > 0 && string(r.AuthorizedGroups) != "null" && string(r.AuthorizedGroups) != "{}") {
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

// EnsurePolicies makes HomersLab's groups and policies for the household's port, and puts back any that drifted. The
// household's own policies and groups stay as they are: NetBird's Default among them, which Conflicts lists for the
// admin's yes, and RemoveDefault deletes only on a NetBird that HomersLab set up itself.
func (m *Mesh) EnsurePolicies(ctx context.Context, port int) error {
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
	for _, r := range mesh.Policies(port) {
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
	return nil
}

// isDefault says whether a policy has the shape of NetBird's Default, whatever its name: an accept of every protocol
// from every peer to every peer.
func isDefault(p policy, all string) bool {
	for _, r := range p.Rules {
		if r.Action == "accept" && r.Protocol == "all" && slices.Contains(r.Sources, all) && slices.Contains(r.Destinations, all) {
			return true
		}
	}
	return false
}

// RemoveDefault deletes the policies shaped like NetBird's Default ("everyone reaches everything"). Only for a
// NetBird that HomersLab set up itself, which holds nothing of the household's yet.
func (m *Mesh) RemoveDefault(ctx context.Context) error {
	ids, err := m.groups(ctx)
	if err != nil {
		return err
	}
	var have []policy
	if err := m.api.call(ctx, http.MethodGet, "/api/policies", nil, &have); err != nil {
		return err
	}
	for _, p := range have {
		if isDefault(p, ids["All"]) {
			if err := m.api.call(ctx, http.MethodDelete, "/api/policies/"+url.PathEscape(p.ID), nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

type resource struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// Conflicts are the accept rules of policies that aren't HomersLab's, are enabled, and reach a node: through the
// group All, through any group a node is in (homerslab-nodes, and any the household put a node in), or through a
// node as a peer resource. A drop rule is never listed: switching it off would widen access.
func (m *Mesh) Conflicts(ctx context.Context) ([]mesh.Policy, error) {
	ids, err := m.groups(ctx)
	if err != nil {
		return nil, err
	}
	reach := map[string]bool{ids["All"]: true, ids[mesh.NodesGroup]: true}
	var peers []struct {
		ID     string  `json:"id"`
		Groups []group `json:"groups"`
	}
	if err := m.api.call(ctx, http.MethodGet, "/api/peers", nil, &peers); err != nil {
		return nil, err
	}
	nodes := map[string]bool{}
	for _, p := range peers {
		if slices.ContainsFunc(p.Groups, func(g group) bool { return g.ID == ids[mesh.NodesGroup] }) {
			nodes[p.ID] = true
			for _, g := range p.Groups {
				reach[g.ID] = true
			}
		}
	}
	delete(reach, "")
	ours := map[string]bool{}
	for _, r := range mesh.Policies(443) { // the names are the same at any port
		ours[r.Name] = true
	}
	var have []policy
	if err := m.api.call(ctx, http.MethodGet, "/api/policies", nil, &have); err != nil {
		return nil, err
	}
	hits := func(groups groupRefs, res *resource) bool {
		return slices.ContainsFunc(groups, func(id string) bool { return reach[id] }) || (res != nil && res.Type == "peer" && nodes[res.ID])
	}
	var out []mesh.Policy
	for _, p := range have {
		if !p.Enabled || ours[p.Name] {
			continue
		}
		for _, r := range p.Rules {
			if r.Enabled && r.Action == "accept" && (hits(r.Destinations, r.DestRes) || (r.Bidirectional && hits(r.Sources, r.SourceRes))) {
				out = append(out, mesh.Policy{ID: p.ID, Name: p.Name})
				break
			}
		}
	}
	return out, nil
}

// Disable switches policies off. Each is written back as NetBird gave it, with only "enabled" changed (and its
// groups as IDs, as the API takes them): its ports, port ranges, posture checks and rule IDs stay, so switching it
// on again in the dashboard brings back exactly what it was.
func (m *Mesh) Disable(ctx context.Context, ids []string) error {
	for _, id := range ids {
		var p map[string]any
		if err := m.api.call(ctx, http.MethodGet, "/api/policies/"+url.PathEscape(id), nil, &p); err != nil {
			return err
		}
		p["enabled"] = false
		rules, _ := p["rules"].([]any)
		for _, r := range rules {
			rule, ok := r.(map[string]any)
			if !ok {
				return fmt.Errorf("policy %s: a rule NetBird's API didn't describe", id)
			}
			for _, k := range []string{"sources", "destinations"} {
				groups, ok := rule[k].([]any)
				if !ok {
					continue // none, or a resource instead
				}
				for i, g := range groups {
					if obj, ok := g.(map[string]any); ok {
						groups[i] = obj["id"]
					}
				}
			}
		}
		if err := m.api.call(ctx, http.MethodPut, "/api/policies/"+url.PathEscape(id), p, nil); err != nil {
			return err
		}
	}
	return nil
}

// NewJoinKey makes a one-off setup key that puts one node in the nodes group, and expires in mesh.JoinKeyLife.
func (m *Mesh) NewJoinKey(ctx context.Context, node string) (mesh.JoinKey, error) {
	return m.newKey(ctx, "homerslab-node-"+node, mesh.NodesGroup)
}

// NewDeviceKey makes a one-off setup key that puts one device in its person's role group, and expires in
// mesh.JoinKeyLife.
func (m *Mesh) NewDeviceKey(ctx context.Context, r mesh.Role, device string) (mesh.JoinKey, error) {
	if !slices.Contains(mesh.Roles, r) {
		return mesh.JoinKey{}, fmt.Errorf("no such role: %q", r)
	}
	return m.newKey(ctx, "homerslab-"+string(r)+"-"+device, mesh.RoleGroup(r))
}

// RevokeKey deletes a setup key; one that is gone already counts as revoked.
func (m *Mesh) RevokeKey(ctx context.Context, id string) error {
	err := m.api.call(ctx, http.MethodDelete, "/api/setup-keys/"+url.PathEscape(id), nil, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return nil
	}
	return err
}

func (m *Mesh) newKey(ctx context.Context, name, group string) (mesh.JoinKey, error) {
	ids, err := m.groups(ctx)
	if err != nil {
		return mesh.JoinKey{}, err
	}
	if ids[group] == "" {
		return mesh.JoinKey{}, fmt.Errorf("the mesh has no group %s yet", group)
	}
	var k struct {
		ID      string    `json:"id"`
		Key     string    `json:"key"`
		Expires time.Time `json:"expires"`
	}
	if err := m.api.call(ctx, http.MethodPost, "/api/setup-keys", map[string]any{
		"name": name, "type": "one-off", "expires_in": int(mesh.JoinKeyLife / time.Second),
		"auto_groups": []string{ids[group]}, "usage_limit": 1, "ephemeral": false,
	}, &k); err != nil {
		return mesh.JoinKey{}, err
	}
	if k.Key == "" {
		return mesh.JoinKey{}, errors.New("NetBird made a setup key without a value")
	}
	return mesh.JoinKey{Value: mesh.Secret(k.Key), ID: k.ID, ExpiresAt: k.Expires}, nil
}

// NewPassword is a password NetBird's embedded sign-in takes: 32 random characters, with every class it may ask for.
func NewPassword() mesh.Secret {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return mesh.Secret(base64.RawURLEncoding.EncodeToString(b) + "-Aa1")
}

// zoneName is the name of HomersLab's DNS zone for a domain, as NetBird's dashboard shows it.
func zoneName(domain string) string { return "homerslab-" + domain }

type zone struct {
	ID                 string    `json:"id,omitempty"`
	Name               string    `json:"name"`
	Domain             string    `json:"domain"`
	Enabled            bool      `json:"enabled"`
	EnableSearchDomain bool      `json:"enable_search_domain"`
	DistributionGroups groupRefs `json:"distribution_groups"`
}

type zoneRecord struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
}

// EnsureNames makes the household's domain a DNS zone on the mesh, given only to HomersLab's groups, with a wildcard
// record at the node's address: every app's name reaches it, and a specific record added later beats the wildcard.
func (m *Mesh) EnsureNames(ctx context.Context, domain string, to netip.Addr) error {
	if !to.IsValid() {
		return errors.New("the names need the node's mesh address")
	}
	ids, err := m.groups(ctx)
	if err != nil {
		return err
	}
	var groups groupRefs
	for _, g := range mesh.NameGroups() {
		groups = append(groups, ids[g])
	}
	want := zone{Name: zoneName(domain), Domain: domain, Enabled: true, EnableSearchDomain: false, DistributionGroups: groups}
	var zones []zone
	if err := m.api.call(ctx, http.MethodGet, "/api/dns/zones", nil, &zones); err != nil {
		return err
	}
	var have *zone
	for i := range zones {
		if strings.EqualFold(zones[i].Domain, domain) {
			if zones[i].Name != want.Name {
				return fmt.Errorf("%w: %s, in the zone %q", mesh.ErrNamesTaken, domain, zones[i].Name)
			}
			have = &zones[i]
		}
	}
	switch {
	case have == nil:
		var made zone
		if err := m.api.call(ctx, http.MethodPost, "/api/dns/zones", want, &made); err != nil {
			return err
		}
		have = &made
	case !have.Enabled || have.EnableSearchDomain || !sortedEqual(have.DistributionGroups, want.DistributionGroups):
		if err := m.api.call(ctx, http.MethodPut, "/api/dns/zones/"+url.PathEscape(have.ID), want, nil); err != nil {
			return err
		}
	}
	path := "/api/dns/zones/" + url.PathEscape(have.ID) + "/records"
	var records []zoneRecord
	if err := m.api.call(ctx, http.MethodGet, path, nil, &records); err != nil {
		return err
	}
	wild := zoneRecord{Name: "*." + domain, Type: "A", Content: to.String(), TTL: 60}
	if to.Is6() {
		wild.Type = "AAAA"
	}
	for _, r := range records {
		if r.Name != wild.Name {
			continue
		}
		if r.Type == wild.Type && r.Content == wild.Content {
			return nil
		}
		return m.api.call(ctx, http.MethodPut, path+"/"+url.PathEscape(r.ID), wild, nil)
	}
	return m.api.call(ctx, http.MethodPost, path, wild, nil)
}
