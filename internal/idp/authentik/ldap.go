package authentik

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/ArjunRachithcf/homerslab/internal/idp"
)

// LDAP (#204, ADR 0020): one outpost of HomersLab's, homerslab-ldap, which the sign-in unit runs beside Authentik on
// the sign-in network; the sign-in listener's port 636 leads to its plain LDAP port. Each LDAP app has its own
// provider there, with a base DN of its own (the outpost picks the provider by the DN a bind names), its own
// application, which only the app's roles (and the app's own account) may open, and its own service account, which
// binds with an app password and may search its provider's directory, and nothing else.
//
// People bind through HomersLab's bind flow, which only an outpost may run (never a browser), and which takes an app
// password only: their passkey can't travel over LDAP, and a password they were given in the console opens nothing.
// The outpost asks Authentik, at every bind, whether the person may open the app (bind and search mode "direct": no
// cache to outlive a change of role).
const (
	LDAPOutpostName = "homerslab-ldap"
	LDAPBindFlow    = "homerslab-ldap-bind"
	stageLDAPID     = "homerslab-ldap-identification"
	stageLDAPPass   = "homerslab-ldap-app-password"
	stageLDAPLogin  = "homerslab-ldap-session"
	// ldapNone is the outpost's own provider, whose application opens to nobody: Authentik keeps no outpost without
	// a provider, and the outpost (so its token) stays while apps come and go.
	ldapNone        = "homerslab-outpost-ldap"
	ldapProvider    = "authentik_providers_ldap.ldapprovider"
	ldapSearch      = "authentik_providers_ldap.search_full_directory"
	appPasswords    = "authentik.core.auth.TokenBackend"
	outpostTokenFmt = "ak-outpost-%s-api"
)

// ldapSlug is an LDAP app's slug: HomersLab's prefix, then the app's id; never the outpost's own.
var ldapSlug = regexp.MustCompile(`^homerslab-[a-z0-9][a-z0-9-]{0,40}$`)

// LDAPBaseDN is an app's directory: dc=<app>,dc=homerslab.
func LDAPBaseDN(slug string) string {
	return "dc=" + strings.TrimPrefix(slug, "homerslab-") + ",dc=homerslab"
}

// LDAPBindDN is a person's (or the app's own account's) DN in an app's directory.
func LDAPBindDN(slug, username string) string {
	return "cn=" + username + ",ou=users," + LDAPBaseDN(slug)
}

// ldapObj is the name of an app's LDAP provider and the slug of its application: the app's slug with -ldap, so an app
// that also has an OIDC client or a guard (homerslab-<app>) keeps it apart.
func ldapObj(slug string) string     { return slug + "-ldap" }
func ldapAccount(slug string) string { return slug + "-ldap" }
func ldapToken(slug string) string   { return slug + "-ldap-bind" }
func ldapRole(slug string) string    { return slug + "-ldap-search" }

var _ idp.LDAP = (*Authentik)(nil)

func validLDAPSlug(slug string) error {
	if !ldapSlug.MatchString(slug) || ldapObj(slug) == ldapNone || strings.HasSuffix(slug, "-") || slug == "homerslab-admin" {
		return fmt.Errorf("authentik: %q isn't an LDAP app's slug", slug)
	}
	return nil
}

// ldapBindFlow makes HomersLab's bind flow, or brings it in line: the username, an app password, the session. Only an
// outpost runs it.
func (a *Authentik) ldapBindFlow(ctx context.Context) (string, error) {
	pks := map[string]string{}
	for _, s := range []struct {
		path, name string
		body       map[string]any
	}{
		{"/stages/identification/", stageLDAPID, map[string]any{"user_fields": []string{"username"}, "password_stage": nil,
			"captcha_stage": nil, "case_insensitive_matching": false, "show_matched_user": false, "enrollment_flow": nil,
			"recovery_flow": nil, "passwordless_flow": nil, "sources": []string{}, "pretend_user_exists": true,
			"enable_remember_me": false, "webauthn_stage": nil}},
		{"/stages/password/", stageLDAPPass, map[string]any{"backends": []string{appPasswords}, "configure_flow": nil,
			"failed_attempts_before_cancel": 3, "allow_show_password": false}},
		{"/stages/user_login/", stageLDAPLogin, map[string]any{"session_duration": "seconds=0", "terminate_other_sessions": false,
			"remember_me_offset": "seconds=0"}},
	} {
		s.body["name"] = s.name
		id, err := a.ensure(ctx, s.path, s.name, s.body)
		if err != nil {
			return "", fmt.Errorf("authentik: the stage %s: %w", s.name, err)
		}
		pks[s.name] = id
	}
	f, err := a.ensureFlow(ctx, LDAPBindFlow, map[string]any{"name": "HomersLab: an app's LDAP bind", "title": "LDAP",
		"designation": "authentication", "authentication": "require_outpost", "denied_action": "message",
		"policy_engine_mode": "all", "compatibility_mode": false, "layout": "stacked"})
	if err != nil {
		return "", err
	}
	if err := a.bindStages(ctx, f, []stageBinding{{Stage: pks[stageLDAPID]}, {Stage: pks[stageLDAPPass]}, {Stage: pks[stageLDAPLogin]}}); err != nil {
		return "", err
	}
	return f, nil
}

// ldapBindIntact says whether the bind flow, if there is one, is exactly HomersLab's: at an outpost only, and bound to
// its three stages alone, in order (the username with no password of its own, an app password only, the session).
// Otherwise a password from the console, or no password at all, could sign a person in to an LDAP app (#204).
func (a *Authentik) ldapBindIntact(ctx context.Context) (bool, error) {
	f, err := a.flow(ctx, LDAPBindFlow)
	if err != nil || f == nil {
		return err == nil, err
	}
	if f.Authentication != "require_outpost" || f.PolicyEngineMode != "all" {
		return false, nil
	}
	type bound struct {
		Stage string `json:"stage"`
		Order int    `json:"order"`
		On    bool   `json:"evaluate_on_plan"`
	}
	bs, err := list[bound](ctx, a.api, "/flows/bindings/?target="+q(f.PK))
	if err != nil {
		return false, err
	}
	type identify struct {
		PK       string   `json:"pk"`
		Name     string   `json:"name"`
		Fields   []string `json:"user_fields"`
		Password *string  `json:"password_stage"`
		Passless *string  `json:"passwordless_flow"`
	}
	type password struct {
		PK       string   `json:"pk"`
		Name     string   `json:"name"`
		Backends []string `json:"backends"`
	}
	type login struct {
		PK   string `json:"pk"`
		Name string `json:"name"`
	}
	id, err1 := named(ctx, a.api, "/stages/identification/?name="+q(stageLDAPID), stageLDAPID, func(i identify) string { return i.Name })
	pw, err2 := named(ctx, a.api, "/stages/password/?name="+q(stageLDAPPass), stageLDAPPass, func(p password) string { return p.Name })
	in, err3 := named(ctx, a.api, "/stages/user_login/?name="+q(stageLDAPLogin), stageLDAPLogin, func(l login) string { return l.Name })
	if err := errors.Join(err1, err2, err3); err != nil || id == nil || pw == nil || in == nil {
		return false, err
	}
	if id.Password != nil || id.Passless != nil || !slices.Equal(id.Fields, []string{"username"}) || !slices.Equal(pw.Backends, []string{appPasswords}) {
		return false, nil
	}
	slices.SortFunc(bs, func(x, y bound) int { return x.Order - y.Order })
	want := []string{id.PK, pw.PK, in.PK}
	if len(bs) != len(want) {
		return false, nil
	}
	for i, b := range bs {
		if b.Stage != want[i] {
			return false, nil
		}
	}
	return true, nil
}

type ldapProviderObj struct {
	PK   int    `json:"pk"`
	Name string `json:"name"`
}

// named is the object of a list whose name is exactly name, or nil: the list's own filter is only a first cut (some of
// Authentik's lists ignore a filter they don't know, and answer with everything).
func named[T any](ctx context.Context, a *api, path, name string, nameOf func(T) string) (*T, error) {
	l, err := list[T](ctx, a, path)
	if err != nil {
		return nil, err
	}
	var found *T
	for i := range l {
		if nameOf(l[i]) == name {
			if found != nil {
				return nil, fmt.Errorf("authentik: two objects named %s at %s", name, path)
			}
			found = &l[i]
		}
	}
	return found, nil
}

func providerName(p ldapProviderObj) string { return p.Name }

// ensureLDAPProvider makes the provider of a base DN, by name, or brings it in line.
func (a *Authentik) ensureLDAPProvider(ctx context.Context, bind, name, baseDN string) (int, bool, error) {
	inval, err := a.flow(ctx, invalidateFlow)
	if err != nil || inval == nil {
		return 0, false, fmt.Errorf("authentik: its default provider flows are missing: %v", err)
	}
	body := map[string]any{"name": name, "authorization_flow": bind, "authentication_flow": nil, "invalidation_flow": inval.PK,
		"base_dn": baseDN, "bind_mode": "direct", "search_mode": "direct", "mfa_support": false, "certificate": nil,
		"tls_server_name": "", "property_mappings": []string{}}
	p, err := named(ctx, a.api, "/providers/ldap/?name__iexact="+q(name), name, providerName)
	if err != nil {
		return 0, false, err
	}
	created := p == nil
	if created {
		p = &ldapProviderObj{}
		err = a.api.call(ctx, http.MethodPost, "/providers/ldap/", body, p)
	} else {
		err = a.api.call(ctx, http.MethodPatch, fmt.Sprintf("/providers/ldap/%d/", p.PK), body, nil)
	}
	return p.PK, created, err
}

type outpostObj struct {
	PK        string `json:"pk"`
	Name      string `json:"name"`
	Providers []int  `json:"providers"`
}

func outpostName(o outpostObj) string { return o.Name }

type roleObj struct {
	PK   string `json:"pk"`
	Name string `json:"name"`
}

func roleName(r roleObj) string { return r.Name }

// ldapOutpost makes HomersLab's LDAP outpost with its own provider, or brings it in line, adding provider when it is
// not 0.
func (a *Authentik) ldapOutpost(ctx context.Context, bind string, add int) (*outpostObj, error) {
	none, _, err := a.ensureLDAPProvider(ctx, bind, ldapNone, "dc=none,dc=homerslab")
	if err != nil {
		return nil, err
	}
	nobody, err := a.audience(ctx, nil) // the policy that refuses everyone
	if err != nil {
		return nil, err
	}
	if err := a.ensureLDAPApp(ctx, ldapNone, "HomersLab LDAP", none, nobody); err != nil {
		return nil, err
	}
	o, err := named(ctx, a.api, "/outposts/instances/?name__iexact="+q(LDAPOutpostName), LDAPOutpostName, outpostName)
	if err != nil {
		return nil, err
	}
	cfg := map[string]any{"authentik_host": a.h.Origin(), "authentik_host_insecure": false, "log_level": "warning"}
	if o == nil {
		o = &outpostObj{}
		ps := []int{none}
		if add != 0 {
			ps = append(ps, add)
		}
		err = a.api.call(ctx, http.MethodPost, "/outposts/instances/", map[string]any{"name": LDAPOutpostName, "type": "ldap",
			"providers": ps, "config": cfg, "service_connection": nil}, o)
		return o, err
	}
	ps := slices.Clone(o.Providers)
	for _, p := range []int{none, add} {
		if p != 0 && !slices.Contains(ps, p) {
			ps = append(ps, p)
		}
	}
	slices.Sort(ps)
	err = a.api.call(ctx, http.MethodPatch, "/outposts/instances/"+o.PK+"/", map[string]any{"type": "ldap", "providers": ps,
		"config": cfg, "service_connection": nil}, nil)
	o.Providers = ps
	return o, err
}

// LDAPOutpost makes HomersLab's LDAP outpost, or brings it in line, and returns its token.
func (a *Authentik) LDAPOutpost(ctx context.Context) (idp.Secret, error) {
	bind, err := a.ldapBindFlow(ctx)
	if err != nil {
		return "", err
	}
	o, err := a.ldapOutpost(ctx, bind, 0)
	if err != nil {
		return "", err
	}
	var k struct {
		Key string `json:"key"`
	}
	if err := a.api.call(ctx, http.MethodGet, "/core/tokens/"+fmt.Sprintf(outpostTokenFmt, o.PK)+"/view_key/", nil, &k); err != nil {
		return "", err
	}
	if k.Key == "" {
		return "", errors.New("authentik: the LDAP outpost has no token")
	}
	return idp.Secret(k.Key), nil
}

// ensureLDAPApp makes the application of an LDAP provider, or brings it in line, opened by exactly these bindings.
func (a *Authentik) ensureLDAPApp(ctx context.Context, slug, name string, provider int, bs []binding) error {
	if len(bs) == 0 {
		return errors.New("authentik: an application with no binding opens to everyone")
	}
	app := map[string]any{"name": name, "slug": slug, "provider": provider, "policy_engine_mode": "any", "meta_hide": true}
	var full struct {
		PK string `json:"pbm_uuid"`
	}
	err := a.api.call(ctx, http.MethodGet, "/core/applications/"+slug+"/", nil, &full)
	switch {
	case notFound(err):
		err = a.api.call(ctx, http.MethodPost, "/core/applications/", app, &full)
	case err == nil:
		err = a.api.call(ctx, http.MethodPatch, "/core/applications/"+slug+"/", app, nil)
	}
	if err != nil {
		return err
	}
	if full.PK == "" {
		return errors.New("authentik: the application has no binding target")
	}
	return a.bindPolicies(ctx, full.PK, bs)
}

// EnsureLDAP makes an app's directory: its provider, its own account with an app password that may search it, and its
// application, which only the app's roles and that account open. The provider joins the outpost last, so nobody binds
// to it before its application is closed to everyone else.
func (a *Authentik) EnsureLDAP(ctx context.Context, app idp.LDAPApp) (idp.LDAPCredentials, error) {
	if err := validLDAPSlug(app.Slug); err != nil {
		return idp.LDAPCredentials{}, err
	}
	if len(app.Roles) == 0 {
		return idp.LDAPCredentials{}, errors.New("authentik: an LDAP app needs roles to open to")
	}
	bs, err := a.audience(ctx, app.Roles)
	if err != nil {
		return idp.LDAPCredentials{}, err
	}
	bind, err := a.ldapBindFlow(ctx)
	if err != nil {
		return idp.LDAPCredentials{}, err
	}
	provider, created, err := a.ensureLDAPProvider(ctx, bind, ldapObj(app.Slug), LDAPBaseDN(app.Slug))
	if err != nil {
		return idp.LDAPCredentials{}, err
	}
	account, err := a.ldapAccount(ctx, app.Slug, app.Name)
	if err != nil {
		return idp.LDAPCredentials{}, err
	}
	password, err := a.ldapPassword(ctx, app.Slug, account)
	if err != nil {
		return idp.LDAPCredentials{}, err
	}
	if err := a.ldapSearchRole(ctx, app.Slug, account, provider); err != nil {
		return idp.LDAPCredentials{}, err
	}
	if err := a.ensureLDAPApp(ctx, ldapObj(app.Slug), app.Name+" (LDAP)", provider, append(bs, binding{User: account})); err != nil {
		return idp.LDAPCredentials{}, err
	}
	if _, err := a.ldapOutpost(ctx, bind, provider); err != nil {
		return idp.LDAPCredentials{}, err
	}
	return idp.LDAPCredentials{BaseDN: LDAPBaseDN(app.Slug), BindDN: LDAPBindDN(app.Slug, ldapAccount(app.Slug)),
		Password: password, Created: created}, nil
}

// ldapAccount makes an app's own LDAP account, a service account in no group, or brings it in line.
func (a *Authentik) ldapAccount(ctx context.Context, slug, name string) (int, error) {
	username := ldapAccount(slug)
	u, err := named(ctx, a.api, "/core/users/?username="+q(username), username, func(u user) string { return u.Username })
	if err != nil {
		return 0, err
	}
	body := map[string]any{"username": username, "name": name + " (LDAP)", "type": "service_account", "is_active": true,
		"groups": []string{}, "path": "homerslab-ldap"}
	if u == nil {
		u = &user{}
		err = a.api.call(ctx, http.MethodPost, "/core/users/", body, u)
	} else {
		err = a.api.call(ctx, http.MethodPatch, fmt.Sprintf("/core/users/%d/", u.PK), body, nil)
	}
	if err != nil {
		return 0, err
	}
	return u.PK, nil
}

// ldapPassword is the app password of an app's own account: made once, never expiring, and kept.
func (a *Authentik) ldapPassword(ctx context.Context, slug string, account int) (idp.Secret, error) {
	id := ldapToken(slug)
	type token struct {
		Identifier string `json:"identifier"`
		Intent     string `json:"intent"`
		User       int    `json:"user"`
		Expiring   bool   `json:"expiring"`
	}
	t, err := named(ctx, a.api, "/core/tokens/?identifier="+q(id), id, func(t token) string { return t.Identifier })
	if err != nil {
		return "", err
	}
	body := map[string]any{"identifier": id, "intent": "app_password", "user": account, "expiring": false,
		"description": "HomersLab: the app's own LDAP bind"}
	switch {
	case t == nil:
		err = a.api.call(ctx, http.MethodPost, "/core/tokens/", body, nil)
	case t.Intent != "app_password" || t.User != account || t.Expiring:
		err = a.api.call(ctx, http.MethodPatch, "/core/tokens/"+id+"/", body, nil)
	}
	if err != nil {
		return "", err
	}
	var k struct {
		Key string `json:"key"`
	}
	if err := a.api.call(ctx, http.MethodGet, "/core/tokens/"+id+"/view_key/", nil, &k); err != nil {
		return "", err
	}
	if k.Key == "" {
		return "", errors.New("authentik: the app's LDAP account has no password")
	}
	return idp.Secret(k.Key), nil
}

// ldapSearchRole gives an app's own account its one permission: to search its own provider's directory.
func (a *Authentik) ldapSearchRole(ctx context.Context, slug string, account, provider int) error {
	r, err := named(ctx, a.api, "/rbac/roles/?search="+q(ldapRole(slug)), ldapRole(slug), roleName)
	if err != nil {
		return err
	}
	if r == nil {
		r = &roleObj{}
		if err := a.api.call(ctx, http.MethodPost, "/rbac/roles/", map[string]any{"name": ldapRole(slug)}, r); err != nil {
			return err
		}
	}
	role := r.PK
	if err := a.api.call(ctx, http.MethodPost, "/rbac/permissions/assigned_by_roles/"+role+"/assign/", map[string]any{
		"permissions": []string{ldapSearch}, "model": ldapProvider, "object_pk": fmt.Sprint(provider)}, nil); err != nil {
		return err
	}
	return a.api.call(ctx, http.MethodPatch, fmt.Sprintf("/core/users/%d/", account), map[string]any{"roles": []string{role}}, nil)
}

// DeleteLDAP deletes an app's directory: its provider leaves the outpost first, so no bind reaches it on the way.
func (a *Authentik) DeleteLDAP(ctx context.Context, slug string) error {
	if err := validLDAPSlug(slug); err != nil {
		return err
	}
	gone := func(err error) error {
		if notFound(err) {
			return nil
		}
		return err
	}
	p, err := named(ctx, a.api, "/providers/ldap/?name__iexact="+q(ldapObj(slug)), ldapObj(slug), providerName)
	if err != nil {
		return err
	}
	if p != nil {
		o, err := named(ctx, a.api, "/outposts/instances/?name__iexact="+q(LDAPOutpostName), LDAPOutpostName, outpostName)
		if err != nil {
			return err
		}
		if o != nil && slices.Contains(o.Providers, p.PK) {
			ps := slices.DeleteFunc(slices.Clone(o.Providers), func(x int) bool { return x == p.PK })
			if err := a.api.call(ctx, http.MethodPatch, "/outposts/instances/"+o.PK+"/", map[string]any{"providers": ps}, nil); err != nil {
				return err
			}
		}
	}
	if err := gone(a.api.call(ctx, http.MethodDelete, "/core/applications/"+ldapObj(slug)+"/", nil, nil)); err != nil {
		return err
	}
	if p != nil {
		if err := gone(a.api.call(ctx, http.MethodDelete, fmt.Sprintf("/providers/ldap/%d/", p.PK), nil, nil)); err != nil {
			return err
		}
	}
	if u, err := named(ctx, a.api, "/core/users/?username="+q(ldapAccount(slug)), ldapAccount(slug), func(u user) string { return u.Username }); err != nil {
		return err
	} else if u != nil {
		if err := gone(a.api.call(ctx, http.MethodDelete, fmt.Sprintf("/core/users/%d/", u.PK), nil, nil)); err != nil {
			return err
		}
	}
	r, err := named(ctx, a.api, "/rbac/roles/?search="+q(ldapRole(slug)), ldapRole(slug), roleName)
	if err != nil || r == nil {
		return err
	}
	return gone(a.api.call(ctx, http.MethodDelete, "/rbac/roles/"+r.PK+"/", nil, nil))
}
