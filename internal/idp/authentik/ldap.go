package authentik

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/idp"
)

// LDAP (#204, ADR 0020): one outpost of HomersLab's, homerslab-ldap, which a unit of its own runs beside the sign-in
// (homerslab-ldap.service) on the sign-in network; the sign-in listener's port 636 leads to its plain LDAP port. Each
// LDAP app has its own provider there, with a base DN of its own (the outpost picks the provider by the DN a bind
// names), its own bind flow, and its own application, which only the app's roles may open.
//
// Nobody searches the household's directory (the maintainer, #204): there is no app account, and no permission to
// search is ever granted, so a person who binds sees their own entry and their own groups, and no one else. Authentik
// has no way to limit a search to an application's audience, and a search of the whole directory would show every
// app the household's people, their mail, and which of them are kids or admins.
//
// People bind through their app's own bind flow, which only an outpost may run (never a browser), and which takes an
// app password made for that app only (AppPassword): their passkey can't travel over LDAP, a password they were given
// in the console opens nothing, and an app that is handed a person's app password can't replay it at another app. The
// outpost asks Authentik, at every bind, whether the person may open the app (bind and search mode "direct": no cache
// to outlive a change of role).
const (
	LDAPOutpostName = "homerslab-ldap"
	stageLDAPID     = "homerslab-ldap-identification"
	stageLDAPPass   = "homerslab-ldap-app-password"
	stageLDAPLogin  = "homerslab-ldap-session"
	// ldapNone is the outpost's own provider, whose application opens to nobody: Authentik keeps no outpost without
	// a provider, and the outpost (so its token) stays while apps come and go. Its base DN is in an ou, which no app's
	// (a dc) can ever be.
	ldapNone     = "homerslab-outpost-ldap"
	ldapNoneDN   = "ou=outpost,dc=homerslab"
	appPasswords = "authentik.core.auth.TokenBackend"
	// ldapClosed marks an app's LDAP application that HomersLab closed to nobody on its way out, with the time it
	// closed: a provider leaves the outpost before its application goes.
	ldapClosed = "homerslab: closed "
	// ldapRetireAfter is how long a closed application and its provider stay after the provider left the outpost,
	// before a later pass deletes them: long past the outpost's taking the change.
	ldapRetireAfter = 5 * time.Minute
	// ldapRefresh is how often the outpost reads its providers again of itself.
	ldapRefresh     = "minutes=1"
	outpostTokenFmt = "ak-outpost-%s-api"
)

// ldapSlug is an LDAP app's slug: HomersLab's prefix, then the app's id; never the outpost's own.
var ldapSlug = regexp.MustCompile(`^homerslab-[a-z0-9][a-z0-9-]{0,40}$`)

// ldapOldFlow is the one bind flow every app shared before each had its own; Prepare removes it.
const ldapOldFlow = "homerslab-ldap-bind"

// ldapFlowSlug is the slug of a bind flow of HomersLab's: an LDAP provider's name (ldapObj, or ldapNone), with -bind.
var ldapFlowSlug = regexp.MustCompile(`^homerslab-[a-z0-9][a-z0-9-]{0,40}-ldap-bind$`)

// LDAPBaseDN is an app's base DN: dc=<app>,dc=homerslab.
func LDAPBaseDN(slug string) string {
	return "dc=" + strings.TrimPrefix(slug, "homerslab-") + ",dc=homerslab"
}

// LDAPUserDN is the DN template of a person in an app's base DN, %s for the username.
func LDAPUserDN(slug string) string { return "cn=%s,ou=users," + LDAPBaseDN(slug) }

// LDAPBindFlow is the slug of an app's bind flow, by the app's slug: homerslab-<app>-ldap-bind.
func LDAPBindFlow(slug string) string { return ldapFlowOf(ldapObj(slug)) }

// ldapObj is the name of an app's LDAP provider and the slug of its application: the app's slug with -ldap, so an app
// that also has an OIDC client or a guard (homerslab-<app>) keeps it apart.
func ldapObj(slug string) string { return slug + "-ldap" }

// ldapFlowOf is the slug of an LDAP provider's bind flow, by the provider's name; ldapPolicyOf the name of the policy
// that lets only that provider's app passwords through it.
func ldapFlowOf(obj string) string   { return obj + "-bind" }
func ldapPolicyOf(obj string) string { return obj + "-app-password" }

// ldapPasswordOf is what the identifier of an app password made for an LDAP provider starts with: the provider's name
// and an underscore, which no app's slug holds, so no app's prefix is the start of another's.
func ldapPasswordOf(obj string) string { return obj + "_" }

// ldapPasswordOnly is the policy on an LDAP provider's session stage: only an app password whose identifier is the
// provider's (ldapPasswordOf) gets a session there. The password stage puts the token it took in the plan; a policy
// that fails, or can't be run, skips the session stage, and the outpost binds nobody without a session.
func ldapPasswordOnly(obj string) string {
	return "# HomersLab (#204): only an app password made for this app binds here.\n" +
		"token = (context.get(\"auth_method_args\") or {}).get(\"token\") or {}\n" +
		"return context.get(\"auth_method\") == \"token\" and str(token.get(\"name\", \"\")).startswith(\"" + ldapPasswordOf(obj) + "\")"
}

var _ idp.LDAP = (*Authentik)(nil)

func validLDAPSlug(slug string) error {
	if !ldapSlug.MatchString(slug) || ldapObj(slug) == ldapNone || strings.HasSuffix(slug, "-") || slug == "homerslab-admin" ||
		LDAPBaseDN(slug) == ldapNoneDN {
		return fmt.Errorf("authentik: %q isn't an LDAP app's slug", slug)
	}
	return nil
}

// ldapStages makes the bind flows' three stages, or brings them in line: the username, an app password, the session.
func (a *Authentik) ldapStages(ctx context.Context) (map[string]string, error) {
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
			return nil, fmt.Errorf("authentik: the stage %s: %w", s.name, err)
		}
		pks[s.name] = id
	}
	return pks, nil
}

// ldapBindFlow makes an LDAP provider's bind flow, by the provider's name, or brings it in line: the username, an app
// password, and the session for an app password made for that provider alone. Only an outpost runs it.
func (a *Authentik) ldapBindFlow(ctx context.Context, obj string) (string, error) {
	pks, err := a.ldapStages(ctx)
	if err != nil {
		return "", err
	}
	only, err := a.ensure(ctx, "/policies/expression/", ldapPolicyOf(obj), map[string]any{"name": ldapPolicyOf(obj),
		"expression": ldapPasswordOnly(obj), "execution_logging": false})
	if err != nil {
		return "", err
	}
	f, err := a.ensureFlow(ctx, ldapFlowOf(obj), map[string]any{"name": "HomersLab: an app's LDAP bind (" + obj + ")", "title": "LDAP",
		"designation": "authentication", "authentication": "require_outpost", "denied_action": "message",
		"policy_engine_mode": "all", "compatibility_mode": false, "layout": "stacked"})
	if err != nil {
		return "", err
	}
	if err := a.bindStages(ctx, f, []stageBinding{{Stage: pks[stageLDAPID]}, {Stage: pks[stageLDAPPass]},
		{Stage: pks[stageLDAPLogin], Policies: []binding{{Policy: only}}}}); err != nil {
		return "", err
	}
	// No policy on the flow itself: only the application's decide who binds.
	fl, err := a.flow(ctx, ldapFlowOf(obj))
	if err != nil || fl == nil {
		return "", fmt.Errorf("authentik: the LDAP bind flow: %v", err)
	}
	if err := a.bindPolicies(ctx, target{PBM: fl.PBM, PK: fl.PK}, nil); err != nil {
		return "", err
	}
	return f, nil
}

// ldapFlows are HomersLab's bind flows, by the names of the providers they are for.
func (a *Authentik) ldapFlows(ctx context.Context) ([]string, error) {
	fs, err := list[flow](ctx, a.api, "/flows/instances/")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range fs {
		if ldapFlowSlug.MatchString(f.Slug) {
			out = append(out, strings.TrimSuffix(f.Slug, "-bind"))
		}
	}
	return out, nil
}

// ldapBindIntact says whether an LDAP provider's bind flow, by the provider's name, is exactly HomersLab's, and which
// flow it is: at an outpost only, bound to its three stages alone, in order, each as bindStages makes it (never decided
// at planning, decided again when reached), with no policy bound to the flow, to the username's or to the password's
// stage (a policy that fails skips its stage: a bind with no password), the session's bound to the provider's own
// app-password policy alone, as HomersLab writes it, and each stage as it should be (the username with no password of
// its own, an app password only, the session). Otherwise a password from the console, no password at all, or an app
// password made for another app could sign a person in to an LDAP app (#204). A flow that isn't there isn't intact.
func (a *Authentik) ldapBindIntact(ctx context.Context, obj string) (bool, string, error) {
	f, err := a.flow(ctx, ldapFlowOf(obj))
	if err != nil || f == nil {
		return false, "", err
	}
	if f.Authentication != "require_outpost" || f.PolicyEngineMode != "all" {
		return false, f.PK, nil
	}
	type bound struct {
		PK      string `json:"pk"`
		PBM     string `json:"policybindingmodel_ptr_id"`
		Stage   string `json:"stage"`
		Order   int    `json:"order"`
		OnPlan  bool   `json:"evaluate_on_plan"`
		ReEval  bool   `json:"re_evaluate_policies"`
		Invalid string `json:"invalid_response_action"`
	}
	bs, err := where[bound](ctx, a.api, "/flows/bindings/?target="+q(f.PK), "target", f.PK)
	if err != nil {
		return false, "", err
	}
	type identify struct {
		PK       string   `json:"pk"`
		Fields   []string `json:"user_fields"`
		Password *string  `json:"password_stage"`
		Passless *string  `json:"passwordless_flow"`
	}
	type password struct {
		PK       string   `json:"pk"`
		Backends []string `json:"backends"`
	}
	type login struct {
		PK string `json:"pk"`
	}
	type expression struct {
		PK   string `json:"pk"`
		Expr string `json:"expression"`
	}
	id, err1 := find[identify](ctx, a.api, "/stages/identification/?name="+q(stageLDAPID), "name", stageLDAPID)
	pw, err2 := find[password](ctx, a.api, "/stages/password/?name="+q(stageLDAPPass), "name", stageLDAPPass)
	in, err3 := find[login](ctx, a.api, "/stages/user_login/?name="+q(stageLDAPLogin), "name", stageLDAPLogin)
	only, err4 := find[expression](ctx, a.api, "/policies/expression/?name="+q(ldapPolicyOf(obj)), "name", ldapPolicyOf(obj))
	if err := errors.Join(err1, err2, err3, err4); err != nil || id == nil || pw == nil || in == nil || only == nil {
		return false, f.PK, err
	}
	// Authentik keeps an expression without the whitespace around it.
	if id.Password != nil || id.Passless != nil || !slices.Equal(id.Fields, []string{"username"}) || !slices.Equal(pw.Backends, []string{appPasswords}) ||
		strings.TrimSpace(only.Expr) != ldapPasswordOnly(obj) {
		return false, f.PK, nil
	}
	slices.SortFunc(bs, func(x, y bound) int { return x.Order - y.Order })
	want := []string{id.PK, pw.PK, in.PK}
	if len(bs) != len(want) {
		return false, f.PK, nil
	}
	type pb struct {
		PK     string  `json:"pk"`
		Policy *string `json:"policy"`
		Group  *string `json:"group"`
		User   *int    `json:"user"`
		Negate bool    `json:"negate"`
		On     bool    `json:"enabled"`
	}
	// The policies each target may have: none on the flow and its first two stages, the provider's own on the session.
	targets := []struct {
		t    target
		only bool
	}{{t: target{PBM: f.PBM, PK: f.PK}}}
	for i, b := range bs {
		if b.Stage != want[i] || b.Order != (i+1)*10 || b.OnPlan || !b.ReEval || b.Invalid != "retry" {
			return false, f.PK, nil
		}
		targets = append(targets, struct {
			t    target
			only bool
		}{t: target{PBM: b.PBM, PK: b.PK}, only: i == len(want)-1})
	}
	for _, t := range targets {
		ps, err := where[pb](ctx, a.api, "/policies/bindings/?target="+q(t.t.PBM), "target", t.t.PK)
		if err != nil {
			return false, f.PK, err
		}
		switch {
		case !t.only && len(ps) > 0:
			return false, f.PK, nil
		case t.only && (len(ps) != 1 || ptrStr(ps[0].Policy) != only.PK || ps[0].Group != nil || ps[0].User != nil || ps[0].Negate || !ps[0].On):
			return false, f.PK, nil
		}
	}
	return true, f.PK, nil
}

// AppPassword makes a person a new app password for one LDAP app (#204): an Authentik token of theirs whose identifier
// is the app's provider's, which the app's bind flow alone takes. It is shown once, here.
func (a *Authentik) AppPassword(ctx context.Context, slug, username string) (idp.Secret, error) {
	if err := validLDAPSlug(slug); err != nil {
		return "", err
	}
	// Only for an app whose directory is there: none waits, made ahead, for an app of that slug to come.
	name := ldapObj(slug)
	p, err := find[ldapProviderObj](ctx, a.api, "/providers/ldap/?name__iexact="+q(name), "name", name)
	if err != nil {
		return "", err
	}
	if p == nil || p.App == nil || *p.App != name {
		return "", fmt.Errorf("authentik: %s has no LDAP directory", slug)
	}
	u, err := find[user](ctx, a.api, "/core/users/?username="+q(username), "username", username)
	if err != nil {
		return "", err
	}
	if u == nil {
		return "", idp.ErrNoPerson
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := ldapPasswordOf(ldapObj(slug)) + hex.EncodeToString(b)
	if err := a.api.call(ctx, http.MethodPost, "/core/tokens/", map[string]any{"identifier": id, "intent": "app_password",
		"user": u.PK, "expiring": false, "description": "HomersLab: an app password for " + slug + " over LDAP"}, nil); err != nil {
		return "", err
	}
	var k struct {
		Key string `json:"key"`
	}
	if err := a.api.call(ctx, http.MethodGet, "/core/tokens/"+id+"/view_key/", nil, &k); err != nil {
		return "", err
	}
	if k.Key == "" {
		return "", errors.New("authentik: the app password has no key")
	}
	return idp.Secret(k.Key), nil
}

type ldapProviderObj struct {
	PK     int     `json:"pk"`
	Name   string  `json:"name"`
	App    *string `json:"assigned_application_slug"`
	BaseDN string  `json:"base_dn"`
	Flow   string  `json:"authorization_flow"`
}

// ensureLDAPProvider makes an LDAP provider of a base DN, by name, or brings it in line: never another application's
// (#219's rule), only its own application's or one no application has yet.
func (a *Authentik) ensureLDAPProvider(ctx context.Context, bind, name, app, baseDN string) (int, bool, error) {
	inval, err := a.flow(ctx, invalidateFlow)
	if err != nil || inval == nil {
		return 0, false, fmt.Errorf("authentik: its default provider flows are missing: %v", err)
	}
	body := map[string]any{"name": name, "authorization_flow": bind, "authentication_flow": nil, "invalidation_flow": inval.PK,
		"base_dn": baseDN, "bind_mode": "direct", "search_mode": "direct", "mfa_support": false, "certificate": nil,
		"tls_server_name": "", "property_mappings": []string{}}
	p, err := find[ldapProviderObj](ctx, a.api, "/providers/ldap/?name__iexact="+q(name), "name", name)
	if err != nil {
		return 0, false, err
	}
	if p != nil && p.App != nil && *p.App != app {
		return 0, false, fmt.Errorf("authentik: the LDAP provider %s is another application's, refused", name)
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

// ldapOutpost makes HomersLab's LDAP outpost with its own provider, or brings it in line, adding provider when it is
// not 0.
func (a *Authentik) ldapOutpost(ctx context.Context, add int) (*outpostObj, error) {
	bind, err := a.ldapBindFlow(ctx, ldapNone)
	if err != nil {
		return nil, err
	}
	none, _, err := a.ensureLDAPProvider(ctx, bind, ldapNone, ldapNone, ldapNoneDN)
	if err != nil {
		return nil, err
	}
	nobody, err := a.audience(ctx, nil) // the policy that refuses everyone
	if err != nil {
		return nil, err
	}
	if err := a.ensureLDAPApp(ctx, ldapNone, "HomersLab LDAP", "", none, nobody); err != nil {
		return nil, err
	}
	o, err := find[outpostObj](ctx, a.api, "/outposts/instances/?name__iexact="+q(LDAPOutpostName), "name", LDAPOutpostName)
	if err != nil {
		return nil, err
	}
	// The outpost also asks for its providers every minute, not only when Authentik tells it they changed: a provider
	// off it is gone from it well within ldapRetireAfter, even when the notice never came.
	cfg := map[string]any{"authentik_host": a.h.Origin(), "authentik_host_insecure": false, "log_level": "warning",
		"refresh_interval": ldapRefresh}
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
	o, err := a.ldapOutpost(ctx, 0)
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

// ensureLDAPApp makes the application of an LDAP provider, or brings it in line, opened by exactly these bindings,
// with desc as its description. A new one is made without its provider, bound, and only then given it: an application
// with no binding opens to everyone. An empty name keeps the one it has.
func (a *Authentik) ensureLDAPApp(ctx context.Context, slug, name, desc string, provider int, bs []binding) error {
	if len(bs) == 0 {
		return errors.New("authentik: an application with no binding opens to everyone")
	}
	var full struct {
		PBM      string `json:"pbm_uuid"`
		PK       string `json:"pk"`
		Name     string `json:"name"`
		Provider *int   `json:"provider"`
	}
	err := a.api.call(ctx, http.MethodGet, "/core/applications/"+slug+"/", nil, &full)
	if err == nil && full.Provider != nil && *full.Provider != provider {
		return fmt.Errorf("%w: %s, refused", errOtherProvider, slug)
	}
	if notFound(err) {
		if name == "" {
			name = slug
		}
		err = a.api.call(ctx, http.MethodPost, "/core/applications/", map[string]any{"name": name, "slug": slug,
			"policy_engine_mode": "any", "meta_hide": true, "meta_description": desc}, &full)
	}
	if err != nil {
		return err
	}
	if full.PBM == "" {
		return errors.New("authentik: the application has no binding target")
	}
	if err := a.bindPolicies(ctx, target{PBM: full.PBM, PK: full.PK}, bs); err != nil {
		return err
	}
	if name == "" {
		name = full.Name
	}
	return a.api.call(ctx, http.MethodPatch, "/core/applications/"+slug+"/", map[string]any{"name": name, "slug": slug,
		"provider": provider, "policy_engine_mode": "any", "meta_hide": true, "meta_description": desc}, nil)
}

// closeLDAPApp closes an LDAP provider's application to nobody, making one if it has none, and marks it closed now. An
// application of that slug that holds another provider is refused (errOtherProvider).
func (a *Authentik) closeLDAPApp(ctx context.Context, slug string, provider int) error {
	nobody, err := a.audience(ctx, nil)
	if err != nil {
		return err
	}
	return a.ensureLDAPApp(ctx, slug, "", ldapClosed+time.Now().UTC().Format(time.RFC3339), provider, nobody)
}

// errOtherProvider is an application, by an LDAP provider's name, that holds another provider.
var errOtherProvider = errors.New("authentik: an LDAP application holds another provider")

// ldapAppState is an LDAP application as the outpost's tidying reads it.
type ldapAppState struct {
	PBM      string `json:"pbm_uuid"`
	PK       string `json:"pk"`
	Desc     string `json:"meta_description"`
	Provider *int   `json:"provider"`
}

// closedAt is when HomersLab closed the application, if it did.
func (s ldapAppState) closedAt() (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, strings.TrimPrefix(s.Desc, ldapClosed))
	return t, strings.HasPrefix(s.Desc, ldapClosed) && err == nil
}

// ldapApp is an LDAP provider's application, by its slug, or nil.
func (a *Authentik) ldapApp(ctx context.Context, slug string) (*ldapAppState, error) {
	var s ldapAppState
	err := a.api.call(ctx, http.MethodGet, "/core/applications/"+slug+"/", nil, &s)
	if notFound(err) {
		return nil, nil
	}
	return &s, err
}

// ldapOwnBindings are the bindings HomersLab gives an LDAP application: a role's group, or the policy that refuses
// everyone. Only what is there now is listed, and nothing is made.
func (a *Authentik) ldapOwnBindings(ctx context.Context) (map[binding]bool, error) {
	own := map[binding]bool{}
	for _, r := range idp.Roles() {
		g, err := find[group](ctx, a.api, "/core/groups/?name="+q(idp.RoleGroup(r)), "name", idp.RoleGroup(r))
		if err != nil {
			return nil, err
		}
		if g != nil {
			own[binding{Group: g.PK}] = true
		}
	}
	never, err := find[struct {
		PK string `json:"pk"`
	}](ctx, a.api, "/policies/expression/?name="+q(policyNever), "name", policyNever)
	if err != nil {
		return nil, err
	}
	if never != nil {
		own[binding{Policy: never.PK}] = true
	}
	return own, nil
}

// ldapBoundByUs says whether an application is opened by HomersLab's bindings alone: at least one, each enabled and
// one of own.
func (a *Authentik) ldapBoundByUs(ctx context.Context, app *ldapAppState, own map[binding]bool) (bool, error) {
	type pb struct {
		Policy *string `json:"policy"`
		Group  *string `json:"group"`
		User   *int    `json:"user"`
		Negate bool    `json:"negate"`
		On     bool    `json:"enabled"`
	}
	bs, err := where[pb](ctx, a.api, "/policies/bindings/?target="+q(app.PBM), "target", app.PK)
	if err != nil || len(bs) == 0 {
		return false, err
	}
	for _, b := range bs {
		if !b.On || b.User != nil || !own[binding{Policy: ptrStr(b.Policy), Group: ptrStr(b.Group), Negate: b.Negate}] {
			return false, nil
		}
	}
	return true, nil
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ldapTidy keeps the outpost serving HomersLab's LDAP providers alone (#204): an app's name, exactly that app's base DN
// (names are unique, so no two share one), its own bind flow as HomersLab makes it (which takes the app's own app
// passwords alone), and its own application, opened by HomersLab's bindings alone. Anything else leaves the outpost,
// its application closed first when it is HomersLab's by name (orphaned providers are removed); the app's next
// EnsureLDAP puts its own back. Closed ones go once off the outpost for a.ldapRetire, with their bind flows and their
// app passwords. With repair false it only reports.
func (a *Authentik) ldapTidy(ctx context.Context, repair bool) (bool, error) {
	o, err := find[outpostObj](ctx, a.api, "/outposts/instances/?name__iexact="+q(LDAPOutpostName), "name", LDAPOutpostName)
	if err != nil || o == nil {
		return err == nil, err
	}
	ps, err := list[ldapProviderObj](ctx, a.api, "/providers/ldap/")
	if err != nil {
		return false, err
	}
	own, err := a.ldapOwnBindings(ctx)
	if err != nil {
		return false, err
	}
	// The outpost's own application opens to nobody: the policy that refuses everyone is its one binding.
	refusesAll := map[binding]bool{}
	for b := range own {
		if b.Policy != "" {
			refusesAll[b] = true
		}
	}
	clean, keep := true, slices.Clone(o.Providers)
	for _, p := range ps {
		on := slices.Contains(o.Providers, p.PK)
		dn, named := ldapNoneDN, p.Name == ldapNone
		if slug := strings.TrimSuffix(p.Name, "-ldap"); !named && slug != p.Name && validLDAPSlug(slug) == nil {
			dn, named = LDAPBaseDN(slug), true
		}
		if !on && !named {
			continue
		}
		var app *ldapAppState
		if p.App != nil && *p.App == p.Name {
			if app, err = a.ldapApp(ctx, p.Name); err != nil {
				return false, err
			}
		}
		fine := named && sameDN(p.BaseDN, dn) && app != nil && app.Provider != nil && *app.Provider == p.PK
		if fine {
			allowed := own
			if p.Name == ldapNone {
				allowed = refusesAll
			}
			if fine, err = a.ldapBoundByUs(ctx, app, allowed); err != nil {
				return false, err
			}
		}
		if fine {
			// Its own bind flow, and no other: another app's would take that app's app passwords here.
			intact, flowPK, err := a.ldapBindIntact(ctx, p.Name)
			if err != nil {
				return false, err
			}
			fine = intact && p.Flow == flowPK
		}
		at, closed := time.Time{}, false
		if app != nil {
			at, closed = app.closedAt()
		}
		switch {
		case p.Name == ldapNone && on:
			// The outpost's own stays: its base DN and its application, closed to nobody, put back.
			if fine && !closed {
				continue
			}
			clean = false
			if !repair {
				continue
			}
			bind, err := a.ldapBindFlow(ctx, ldapNone)
			if err != nil {
				return false, err
			}
			if err := a.api.call(ctx, http.MethodPatch, fmt.Sprintf("/providers/ldap/%d/", p.PK),
				map[string]any{"base_dn": ldapNoneDN, "authorization_flow": bind}, nil); err != nil {
				return false, err
			}
			nobody, err := a.audience(ctx, nil)
			if err != nil {
				return false, err
			}
			if err := a.ensureLDAPApp(ctx, ldapNone, "HomersLab LDAP", "", p.PK, nobody); err != nil {
				return false, err
			}
		case on && (!fine || closed):
			clean = false
			if !repair {
				continue
			}
			if named && (app != nil || p.App == nil) {
				// An application holding another provider is left as it is: the provider leaves the outpost all the same.
				if err := a.closeLDAPApp(ctx, p.Name, p.PK); err != nil && !errors.Is(err, errOtherProvider) {
					return false, err
				}
			}
			keep = slices.DeleteFunc(keep, func(x int) bool { return x == p.PK })
		case !on && named && p.Name != ldapNone && repair && (p.App == nil || closed && time.Since(at) >= a.ldapRetire):
			if p.App != nil {
				if err := gone(a.api.call(ctx, http.MethodDelete, "/core/applications/"+p.Name+"/", nil, nil)); err != nil {
					return false, err
				}
			}
			if err := gone(a.api.call(ctx, http.MethodDelete, fmt.Sprintf("/providers/ldap/%d/", p.PK), nil, nil)); err != nil {
				return false, err
			}
			if err := a.ldapForget(ctx, p.Name); err != nil {
				return false, err
			}
		}
	}
	if len(keep) != len(o.Providers) {
		if err := a.offOutpost(ctx, o, keep); err != nil {
			return false, err
		}
	}
	return clean, nil
}

// ldapForget deletes what an LDAP provider that is gone leaves behind: the app passwords made for it, its bind flow and
// the flow's policy.
func (a *Authentik) ldapForget(ctx context.Context, obj string) error {
	type token struct {
		Identifier string `json:"identifier"`
	}
	ts, err := list[token](ctx, a.api, "/core/tokens/?intent=app_password")
	if err != nil {
		return err
	}
	for _, t := range ts {
		if strings.HasPrefix(t.Identifier, ldapPasswordOf(obj)) {
			if err := gone(a.api.call(ctx, http.MethodDelete, "/core/tokens/"+t.Identifier+"/", nil, nil)); err != nil {
				return err
			}
		}
	}
	if err := gone(a.api.call(ctx, http.MethodDelete, "/flows/instances/"+ldapFlowOf(obj)+"/", nil, nil)); err != nil {
		return err
	}
	pol, err := find[struct {
		PK string `json:"pk"`
	}](ctx, a.api, "/policies/expression/?name="+q(ldapPolicyOf(obj)), "name", ldapPolicyOf(obj))
	if err != nil || pol == nil {
		return err
	}
	return gone(a.api.call(ctx, http.MethodDelete, "/policies/expression/"+pol.PK+"/", nil, nil))
}

// sameDN says whether a base DN is exactly HomersLab's: ASCII only, compared without case.
func sameDN(got, want string) bool {
	for i := range len(got) {
		if got[i] >= 0x80 {
			return false
		}
	}
	return strings.ToLower(got) == want
}

// offOutpost leaves the outpost with these providers only.
func (a *Authentik) offOutpost(ctx context.Context, o *outpostObj, keep []int) error {
	return a.api.call(ctx, http.MethodPatch, "/outposts/instances/"+o.PK+"/", map[string]any{"providers": keep}, nil)
}

// gone is nil for an object that isn't there.
func gone(err error) error {
	if notFound(err) {
		return nil
	}
	return err
}

// EnsureLDAP makes an app's LDAP provider and its application, which only the app's roles open. The provider joins the
// outpost last, so nobody binds to it before its application is closed to everyone else.
func (a *Authentik) EnsureLDAP(ctx context.Context, app idp.LDAPApp) (idp.LDAPCredentials, error) {
	creds, err := a.ensureLDAP(ctx, app)
	if err != nil {
		return idp.LDAPCredentials{}, err
	}
	if _, err := a.ldapTidy(ctx, true); err != nil {
		return idp.LDAPCredentials{}, err
	}
	return creds, nil
}

// EnsureLDAPApps brings each of these apps' LDAP providers in line, as EnsureLDAP does, and tidies the outpost once,
// after them all: a tidy reads every provider, so one per app would cost the pass a number of calls that grows as the
// square of the apps (#246's review). One app's failure keeps neither the others' nor the tidy from theirs.
func (a *Authentik) EnsureLDAPApps(ctx context.Context, apps []idp.LDAPApp) error {
	var errs []error
	for _, app := range apps {
		if _, err := a.ensureLDAP(ctx, app); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", app.Slug, err))
		}
	}
	if _, err := a.ldapTidy(ctx, true); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ensureLDAP is EnsureLDAP without the outpost's tidy.
func (a *Authentik) ensureLDAP(ctx context.Context, app idp.LDAPApp) (idp.LDAPCredentials, error) {
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
	bind, err := a.ldapBindFlow(ctx, ldapObj(app.Slug))
	if err != nil {
		return idp.LDAPCredentials{}, err
	}
	provider, created, err := a.ensureLDAPProvider(ctx, bind, ldapObj(app.Slug), ldapObj(app.Slug), LDAPBaseDN(app.Slug))
	if err != nil {
		return idp.LDAPCredentials{}, err
	}
	if err := a.ensureLDAPApp(ctx, ldapObj(app.Slug), app.Name+" (LDAP)", "", provider, bs); err != nil {
		return idp.LDAPCredentials{}, err
	}
	if _, err := a.ldapOutpost(ctx, provider); err != nil {
		return idp.LDAPCredentials{}, err
	}
	return idp.LDAPCredentials{BaseDN: LDAPBaseDN(app.Slug), UserDN: LDAPUserDN(app.Slug), Created: created}, nil
}

// DeleteLDAP closes an app's LDAP application to nobody, at once, then takes its provider off the outpost: a provider
// leaves the outpost before its application goes. The two are deleted by a later pass (ldapTidy), once the outpost
// can no longer be serving the provider. Another application's provider is never touched.
func (a *Authentik) DeleteLDAP(ctx context.Context, slug string) error {
	if err := validLDAPSlug(slug); err != nil {
		return err
	}
	name := ldapObj(slug)
	p, err := find[ldapProviderObj](ctx, a.api, "/providers/ldap/?name__iexact="+q(name), "name", name)
	if err != nil {
		return err
	}
	if p != nil && p.App != nil && *p.App != name {
		return fmt.Errorf("authentik: the LDAP provider %s is another application's, refused", name)
	}
	if p == nil {
		// No provider: an application left without one opens nothing to bind to.
		if err := gone(a.api.call(ctx, http.MethodDelete, "/core/applications/"+name+"/", nil, nil)); err != nil {
			return err
		}
		_, err := a.ldapTidy(ctx, true)
		return err
	}
	o, err := find[outpostObj](ctx, a.api, "/outposts/instances/?name__iexact="+q(LDAPOutpostName), "name", LDAPOutpostName)
	if err != nil {
		return err
	}
	on := o != nil && slices.Contains(o.Providers, p.PK)
	app, err := a.ldapApp(ctx, name)
	if err != nil {
		return err
	}
	closed := false
	if app != nil {
		_, closed = app.closedAt()
	}
	// Closed already and off the outpost: kept as it is, so its time off the outpost runs on.
	if on || !closed {
		if err := a.closeLDAPApp(ctx, name, p.PK); err != nil {
			return err
		}
	}
	if on {
		if err := a.offOutpost(ctx, o, slices.DeleteFunc(slices.Clone(o.Providers), func(x int) bool { return x == p.PK })); err != nil {
			return err
		}
	}
	_, err = a.ldapTidy(ctx, true)
	return err
}
