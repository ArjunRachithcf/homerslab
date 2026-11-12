// Package authentik is the identity provider adapter for Authentik (ADR 0004): the household account, as HomersLab's
// own service account in it, through Authentik's REST API. It passes the identity provider contract suite against a
// real Authentik in containers (contract_test.go).
//
// HomersLab owns these objects, named homerslab-*, and corrects them when they drift:
//   - the groups for roles;
//   - the passkey-only sign-in flow: identification without a password, then a passkey with user verification
//     required, then the session. It is the brand's sign-in, and every client's that is passkey-only;
//   - the passkey-enrolment flow, run only from a one-time link: an invitation for one person, then a passkey with
//     user verification required. It is never the brand's recovery flow, and it has no other stage;
//   - the brand of the sign-in's own name;
//   - in every other flow that signs a person in or sets up a way to (Authentik's default authentication flow and
//     its authenticator setup flows among them), a refusal for the household's people before the first stage that
//     does: they sign in with their passkey, through HomersLab's flow, and get a passkey only from an enrolment
//     link.
//
// Authentik's own first-admin flow (initial-setup) applies to nobody: akadmin has a random password and is inactive,
// the flow wants a superuser, and a policy that refuses everyone is bound to it.
package authentik

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/idp"
)

// Version is the Authentik that HomersLab's packages pin (packaging/sign-in/authentik.compose.yaml).
const Version = "2026.8.5"

// HomersLab's objects in Authentik.
const (
	ServiceUser = "homerslab"
	tokenID     = "homerslab-manager"
	// The flows, by slug.
	SignInFlow    = "homerslab-sign-in"
	EnrolmentFlow = "homerslab-passkey-enrolment"
	// Stages, policies and the never policy, by name.
	stageIdentify   = "homerslab-sign-in-identification"
	stagePasskey    = "homerslab-sign-in-passkey"
	stageLogin      = "homerslab-sign-in-session"
	stageLink       = "homerslab-enrolment-link"
	stageRefused    = "homerslab-enrolment-refused"
	stageEnrol      = "homerslab-enrolment-passkey"
	stageAfter      = "homerslab-enrolment-done"
	policyPerson    = "homerslab-enrolment-person"
	stageNotHere    = "homerslab-passkey-only"
	policyHousehold = "homerslab-household-person"
	loginStageModel = "authentik_stages_user_login.userloginstage"
	writeStageModel = "authentik_stages_user_write.userwritestage"
	// Every authenticator stage, validation included: a validation stage can set one up on the way.
	authenticatorModels = "authentik_stages_authenticator_"
	policyNever         = "homerslab-never"
	firstAdminFlow      = "initial-setup"
	firstAdmin          = "akadmin"
	bootstrapToken      = "authentik-bootstrap-token"
	superuserGroup      = "authentik Admins"
	personField         = "homerslab_person"
	consentFlow         = "default-provider-authorization-implicit-consent"
	invalidateFlow      = "default-provider-invalidation-flow"
	selfSignedCert      = "authentik Self-signed Certificate"
	userPath            = "homerslab"
	enrolmentPrefix     = "homerslab-enrol-"
)

// Authentik is the household account, as HomersLab's service account.
type Authentik struct {
	api *api
	h   idp.Household
	// Landing is where a finished enrolment goes: Admin, for an admin.
	landing string
}

// Open is Authentik at its upstream base (http://<its bridge address>:9000), as the sign-in of household h, with
// HomersLab's own token. landing is where a finished enrolment sends the person.
func Open(base string, h idp.Household, token idp.Secret, landing string) (*Authentik, error) {
	a, err := newAPI(base, h, token)
	if err != nil {
		return nil, err
	}
	return &Authentik{api: a, h: h, landing: landing}, nil
}

var _ idp.Provider = (*Authentik)(nil)

// Ready says whether Authentik answers and has finished starting: its readiness endpoint, then the API.
func Ready(ctx context.Context, base string, h idp.Household) error {
	a, err := newAPI(base, h, "")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base.JoinPath("/-/health/ready/").String(), nil)
	if err != nil {
		return err
	}
	req.Host = a.host
	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", idp.ErrUnreachable, err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%w: its readiness check answered %d", idp.ErrNotReady, resp.StatusCode)
	}
	return nil
}

type user struct {
	PK       int            `json:"pk"`
	Username string         `json:"username"`
	Name     string         `json:"name"`
	Email    string         `json:"email"`
	IsActive bool           `json:"is_active"`
	Type     string         `json:"type"`
	Groups   []string       `json:"groups"`
	Attrs    map[string]any `json:"attributes"`
	Path     string         `json:"path"`
}

type group struct {
	PK   string `json:"pk"`
	Name string `json:"name"`
}

// Bootstrap makes HomersLab's own service account and token with Authentik's bootstrap token, and closes Authentik's
// own way to a first admin. HomersLab's token goes to keep (the caller seals it) before anything else changes; only
// then are akadmin closed and the bootstrap token deleted, so a token that couldn't be kept is never the only one. A bootstrap token that is refused means bootstrap is done already (or
// never was given): ErrTokenRefused.
//
// It runs before the edge serves the sign-in's name, so nobody can reach Authentik's first-admin flow in between.
func Bootstrap(ctx context.Context, base string, h idp.Household, bootstrap idp.Secret, keep func(idp.Secret) error) (idp.Secret, error) {
	b, err := newAPI(base, h, bootstrap)
	if err != nil {
		return "", err
	}
	if err := b.call(ctx, http.MethodGet, "/core/users/me/", nil, nil); err != nil {
		return "", err
	}
	if err := defaultsReady(ctx, b); err != nil {
		return "", err
	}
	admins, err := one[group](ctx, b, "/core/groups/?name="+q(superuserGroup))
	if err != nil {
		return "", err
	}
	if admins == nil {
		return "", errors.New("authentik: its superuser group is missing")
	}
	svc, err := one[user](ctx, b, "/core/users/?username="+q(ServiceUser))
	if err != nil {
		return "", err
	}
	body := map[string]any{"username": ServiceUser, "name": "HomersLab", "type": "service_account", "is_active": true,
		"groups": []string{admins.PK}, "path": userPath}
	if svc == nil {
		svc = &user{}
		if err := b.call(ctx, http.MethodPost, "/core/users/", body, svc); err != nil {
			return "", err
		}
	} else if err := b.call(ctx, http.MethodPatch, fmt.Sprintf("/core/users/%d/", svc.PK), body, nil); err != nil {
		return "", err
	}
	// A token left by a bootstrap that stopped half-way is replaced: its key was never kept.
	_ = b.call(ctx, http.MethodDelete, "/core/tokens/"+tokenID+"/", nil, nil)
	if err := b.call(ctx, http.MethodPost, "/core/tokens/", map[string]any{"identifier": tokenID, "intent": "api",
		"user": svc.PK, "expiring": false, "description": "HomersLab's manager: sealed on the primary"}, nil); err != nil {
		return "", err
	}
	var key struct{ Key string }
	if err := b.call(ctx, http.MethodGet, "/core/tokens/"+tokenID+"/view_key/", nil, &key); err != nil {
		return "", err
	}
	own := idp.Secret(key.Key)
	o, err := newAPI(base, h, own)
	if err != nil {
		return "", err
	}
	var check struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := o.call(ctx, http.MethodGet, "/core/users/me/", nil, &check); err != nil || check.User.Username != ServiceUser {
		return "", fmt.Errorf("authentik: HomersLab's new token doesn't work: %v", err)
	}
	if err := keep(own); err != nil {
		return "", fmt.Errorf("keeping HomersLab's new token: %w", err) // the bootstrap token still works: try again
	}
	a := &Authentik{api: o, h: h}
	if err := a.closeFirstAdmin(ctx); err != nil {
		return "", err
	}
	if err := o.call(ctx, http.MethodDelete, "/core/tokens/"+bootstrapToken+"/", nil, nil); err != nil && !notFound(err) {
		return "", fmt.Errorf("authentik: deleting the bootstrap token: %w", err)
	}
	return own, nil
}

// DefaultFlows are the flows Authentik's own default blueprints make, at Version: those HomersLab builds on (consent,
// invalidation), closes (initial-setup), and guards (Prepare's refusal goes into every one that signs a person in or
// sets something up for them).
var DefaultFlows = []string{"default-authentication-flow", "default-authenticator-static-setup", "default-authenticator-totp-setup",
	"default-authenticator-webauthn-setup", "default-invalidation-flow", "default-provider-authorization-explicit-consent",
	consentFlow, invalidateFlow, "default-request", "default-source-authentication", "default-source-enrollment",
	"default-source-pre-authentication", "default-user-settings-flow", firstAdminFlow, "default-password-change"}

// defaultsReady says whether Authentik's worker has made its default objects, which HomersLab's own build on or
// guard: every flow of its default blueprints, with every default and system blueprint applied (they come one by one,
// a fresh Authentik answers before they are), and the signing key. Prepare guards the flows it finds, so it must not
// run before they all are there.
func defaultsReady(ctx context.Context, a *api) error {
	for _, slug := range DefaultFlows {
		f, err := one[flow](ctx, a, "/flows/instances/?slug="+q(slug))
		if err != nil {
			return err
		}
		if f == nil {
			return fmt.Errorf("%w: its default flow %s isn't made yet", idp.ErrNotReady, slug)
		}
	}
	type instance struct {
		Path    string `json:"path"`
		Status  string `json:"status"`
		Enabled bool   `json:"enabled"`
	}
	bs, err := list[instance](ctx, a, "/managed/blueprints/")
	if err != nil {
		return err
	}
	n := 0
	for _, b := range bs {
		if !b.Enabled || !(strings.HasPrefix(b.Path, "default/") || strings.HasPrefix(b.Path, "system/")) {
			continue
		}
		n++
		if b.Status != "successful" {
			return fmt.Errorf("%w: its blueprint %s is %s", idp.ErrNotReady, b.Path, b.Status)
		}
	}
	if n == 0 {
		return fmt.Errorf("%w: it hasn't found its blueprints yet", idp.ErrNotReady)
	}
	cert, err := one[struct {
		PK string `json:"pk"`
	}](ctx, a, "/crypto/certificatekeypairs/?name="+q(selfSignedCert))
	if err != nil {
		return err
	}
	if cert == nil {
		return fmt.Errorf("%w: its signing key isn't made yet", idp.ErrNotReady)
	}
	return nil
}

func notFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// closeFirstAdmin makes Authentik's own first-admin flow apply to nobody, the executor API included (#84): akadmin
// gets a random password and is deactivated (it stays, inactive, so nothing recreates a superuser in its name), the
// flow wants a superuser, and a policy that refuses everyone is bound to it.
func (a *Authentik) closeFirstAdmin(ctx context.Context) error {
	ak, err := one[user](ctx, a.api, "/core/users/?username="+q(firstAdmin))
	if err != nil {
		return err
	}
	if ak != nil {
		if err := a.api.call(ctx, http.MethodPost, fmt.Sprintf("/core/users/%d/set_password/", ak.PK),
			map[string]string{"password": randomPassword()}, nil); err != nil {
			return fmt.Errorf("authentik: akadmin's password: %w", err)
		}
		if err := a.api.call(ctx, http.MethodPatch, fmt.Sprintf("/core/users/%d/", ak.PK), map[string]any{"is_active": false}, nil); err != nil {
			return fmt.Errorf("authentik: deactivating akadmin: %w", err)
		}
	}
	f, err := a.flow(ctx, firstAdminFlow)
	if err != nil || f == nil {
		return err // no first-admin flow: nothing to close
	}
	if err := a.api.call(ctx, http.MethodPatch, "/flows/instances/"+firstAdminFlow+"/", map[string]any{"authentication": "require_superuser"}, nil); err != nil {
		return err
	}
	never, err := a.ensure(ctx, "/policies/expression/", policyNever, map[string]any{"name": policyNever,
		"expression": "# HomersLab: this flow applies to nobody.\nreturn False\n"})
	if err != nil {
		return err
	}
	return a.bindPolicies(ctx, f.PBM, []binding{{Policy: never}})
}

// randomPassword is a password nobody knows or keeps: it only makes the account's password usable, so a flow that
// takes "no usable password" as its condition never applies.
func randomPassword() string {
	b := make([]byte, 48)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

type flow struct {
	PK string `json:"pk"`
	// PBM is the flow as a target of policy bindings.
	PBM            string `json:"policybindingmodel_ptr_id"`
	Slug           string `json:"slug"`
	Designation    string `json:"designation"`
	Authentication string `json:"authentication"`
}

func (a *Authentik) flow(ctx context.Context, slug string) (*flow, error) {
	return one[flow](ctx, a.api, "/flows/instances/?slug="+q(slug))
}

// ensure makes the object named name at a list endpoint, or brings it in line with body, and returns its pk.
func (a *Authentik) ensure(ctx context.Context, path, name string, body map[string]any) (string, error) {
	type obj struct {
		PK json.RawMessage `json:"pk"`
	}
	have, err := one[obj](ctx, a.api, path+"?name="+q(name))
	if err != nil {
		return "", err
	}
	var out obj
	if have == nil {
		err = a.api.call(ctx, http.MethodPost, path, body, &out)
	} else {
		out = *have
		err = a.api.call(ctx, http.MethodPatch, path+pk(have.PK)+"/", body, nil)
	}
	if err != nil {
		return "", err
	}
	return pk(out.PK), nil
}

func pk(raw json.RawMessage) string { return strings.Trim(string(raw), `"`) }

type binding struct {
	Policy string `json:"policy,omitempty"`
	Group  string `json:"group,omitempty"`
	Negate bool   `json:"negate"`
}

// bindPolicies makes exactly these policy bindings on a target (a flow, a stage binding, an application), in order.
func (a *Authentik) bindPolicies(ctx context.Context, target string, want []binding) error {
	type pb struct {
		PK     string `json:"pk"`
		Policy string `json:"policy"`
		Group  string `json:"group"`
		Negate bool   `json:"negate"`
		Order  int    `json:"order"`
		On     bool   `json:"enabled"`
	}
	have, err := list[pb](ctx, a.api, "/policies/bindings/?target="+q(target))
	if err != nil {
		return err
	}
	kept := make([]bool, len(want))
	for _, h := range have {
		i := slices.IndexFunc(want, func(w binding) bool { return w.Policy == h.Policy && w.Group == h.Group && w.Negate == h.Negate })
		if i >= 0 && !kept[i] && h.On && h.Order == i*10 {
			kept[i] = true
			continue
		}
		if err := a.api.call(ctx, http.MethodDelete, "/policies/bindings/"+h.PK+"/", nil, nil); err != nil {
			return err
		}
	}
	for i, w := range want {
		if kept[i] {
			continue
		}
		body := map[string]any{"target": target, "order": i * 10, "enabled": true, "negate": w.Negate, "timeout": 30}
		if w.Policy != "" {
			body["policy"] = w.Policy
		}
		if w.Group != "" {
			body["group"] = w.Group
		}
		if err := a.api.call(ctx, http.MethodPost, "/policies/bindings/", body, nil); err != nil {
			return err
		}
	}
	return nil
}

// stageBinding is one stage of a flow, with the policies bound to that binding.
type stageBinding struct {
	Stage    string
	Policies []binding
}

// bindStages makes exactly these stages a flow's, in order (10, 20, …). A stage with policies has them re-evaluated
// when it is reached, never at planning: they read what earlier stages put in the plan.
func (a *Authentik) bindStages(ctx context.Context, flowPK string, want []stageBinding) error {
	type fsb struct {
		PK      string `json:"pk"`
		PBM     string `json:"policybindingmodel_ptr_id"`
		Stage   string `json:"stage"`
		Order   int    `json:"order"`
		OnPlan  bool   `json:"evaluate_on_plan"`
		ReEval  bool   `json:"re_evaluate_policies"`
		Invalid string `json:"invalid_response_action"`
	}
	have, err := list[fsb](ctx, a.api, "/flows/bindings/?target="+q(flowPK))
	if err != nil {
		return err
	}
	kept := map[int]string{}
	for _, h := range have {
		i := slices.IndexFunc(want, func(w stageBinding) bool { return w.Stage == h.Stage })
		if i >= 0 && h.Order == (i+1)*10 && !h.OnPlan && h.ReEval && h.Invalid == "retry" {
			if _, dup := kept[i]; !dup {
				kept[i] = h.PBM
				continue
			}
		}
		if err := a.api.call(ctx, http.MethodDelete, "/flows/bindings/"+h.PK+"/", nil, nil); err != nil {
			return err
		}
	}
	for i, w := range want {
		id, ok := kept[i]
		if !ok {
			var out struct {
				PBM string `json:"policybindingmodel_ptr_id"`
			}
			if err := a.api.call(ctx, http.MethodPost, "/flows/bindings/", map[string]any{"target": flowPK, "stage": w.Stage,
				"order": (i + 1) * 10, "evaluate_on_plan": false, "re_evaluate_policies": true, "invalid_response_action": "retry",
				"policy_engine_mode": "all"}, &out); err != nil {
				return err
			}
			id = out.PBM
		}
		if err := a.bindPolicies(ctx, id, w.Policies); err != nil {
			return err
		}
	}
	return nil
}

// ensureFlow makes a flow of HomersLab's, by slug, or brings it in line.
func (a *Authentik) ensureFlow(ctx context.Context, slug string, body map[string]any) (string, error) {
	body["slug"] = slug
	f, err := a.flow(ctx, slug)
	if err != nil {
		return "", err
	}
	if f == nil {
		var out flow
		if err := a.api.call(ctx, http.MethodPost, "/flows/instances/", body, &out); err != nil {
			return "", err
		}
		return out.PK, nil
	}
	return f.PK, a.api.call(ctx, http.MethodPatch, "/flows/instances/"+slug+"/", body, nil)
}

// enrolmentPerson is the policy that makes an enrolment link's person the flow's pending user: only from a valid,
// unexpired invitation for HomersLab's enrolment flow, for an active person of the household. Anything else refuses.
// Authentik doesn't check an invitation's expiry when it is used, only when a cleanup task runs, so this does.
const enrolmentPerson = `# HomersLab: an enrolment link's person, from its invitation, and nothing else.
from django.utils import timezone
from authentik.core.models import User
plan = request.context.get("flow_plan")
if plan is None:
    return False
invite = plan.context.get("invitation")
if invite is None or not plan.context.get("invitation_in_effect"):
    return False
if invite.flow is None or invite.flow.slug != "` + EnrolmentFlow + `":
    return False
if invite.expires is None or invite.expires <= timezone.now():
    ak_message("This link has expired. Ask for a new one.")
    return False
username = (invite.fixed_data or {}).get("` + personField + `")
if not username:
    return False
person = User.objects.filter(username=username, is_active=True, type="internal", path="` + userPath + `").first()
if person is None:
    return False
plan.context["pending_user"] = person
return True
`

// householdPerson is the policy that knows the household's people: HomersLab's users, and anyone in a role's group.
var householdPerson = `# HomersLab: a person of the household, who signs in with a passkey through HomersLab's sign-in only.
user = request.user
if not getattr(user, "pk", None):
    return False
if getattr(user, "path", "") == "` + userPath + `":
    return True
return user.all_groups().filter(name__in=["` + strings.Join(roleGroups(), `", "`) + `"]).exists()
`

func roleGroups() []string {
	var gs []string
	for _, r := range idp.Roles() {
		gs = append(gs, idp.RoleGroup(r))
	}
	return gs
}

// Prepare makes the household's sign-in. See the package's comment for what it holds.
func (a *Authentik) Prepare(ctx context.Context, h idp.Household) error {
	if h.SignIn() != a.h.SignIn() || h.Origin() != a.h.Origin() {
		return errors.New("authentik: the household's sign-in moved; renaming the main domain is a migration of its own")
	}
	a.h = h
	if err := defaultsReady(ctx, a.api); err != nil {
		return err
	}
	if err := a.closeFirstAdmin(ctx); err != nil {
		return err
	}
	for _, r := range idp.Roles() {
		if _, err := a.ensure(ctx, "/core/groups/", idp.RoleGroup(r), map[string]any{"name": idp.RoleGroup(r), "is_superuser": false}); err != nil {
			return err
		}
	}
	steps := []struct {
		path, name string
		body       map[string]any
	}{
		{"/stages/identification/", stageIdentify, map[string]any{"user_fields": []string{"username", "email"}, "password_stage": nil,
			"captcha_stage": nil, "case_insensitive_matching": true, "show_matched_user": true, "enrollment_flow": nil,
			"recovery_flow": nil, "passwordless_flow": nil, "sources": []string{}, "pretend_user_exists": true,
			"enable_remember_me": false, "webauthn_stage": nil}},
		{"/stages/authenticator/validate/", stagePasskey, map[string]any{"device_classes": []string{"webauthn"},
			"not_configured_action": "deny", "webauthn_user_verification": "required", "configuration_stages": []string{},
			"last_auth_threshold": "seconds=0", "webauthn_allowed_device_types": []string{}}},
		{"/stages/user_login/", stageLogin, map[string]any{"session_duration": "hours=12", "terminate_other_sessions": false,
			"remember_me_offset": "seconds=0"}},
		{"/stages/invitation/stages/", stageLink, map[string]any{"continue_flow_without_invitation": false}},
		{"/stages/deny/", stageRefused, map[string]any{"deny_message": "This link doesn't work: it was used already, or it expired. Ask for a new one."}},
		{"/stages/authenticator/webauthn/", stageEnrol, map[string]any{"friendly_name": "Passkey", "user_verification": "required",
			"resident_key_requirement": "required", "authenticator_attachment": nil, "hints": []string{},
			"device_type_restrictions": []string{}, "max_attempts": 5, "configure_flow": nil}},
		{"/stages/redirect/", stageAfter, map[string]any{"mode": "static", "target_static": a.landing, "keep_context": false}},
		{"/stages/deny/", stageNotHere, map[string]any{"deny_message": "Sign in with your passkey."}},
	}
	pks := map[string]string{}
	for _, s := range steps {
		s.body["name"] = s.name
		id, err := a.ensure(ctx, s.path, s.name, s.body)
		if err != nil {
			return fmt.Errorf("authentik: the stage %s: %w", s.name, err)
		}
		pks[s.name] = id
	}
	person, err := a.ensure(ctx, "/policies/expression/", policyPerson, map[string]any{"name": policyPerson, "expression": enrolmentPerson})
	if err != nil {
		return err
	}
	title := "Sign in to " + h.Name
	signIn, err := a.ensureFlow(ctx, SignInFlow, map[string]any{"name": "HomersLab: sign in with a passkey", "title": title,
		"designation": "authentication", "authentication": "none", "denied_action": "message", "policy_engine_mode": "all",
		"compatibility_mode": false, "layout": "stacked"})
	if err != nil {
		return err
	}
	if err := a.bindStages(ctx, signIn, []stageBinding{{Stage: pks[stageIdentify]}, {Stage: pks[stagePasskey]}, {Stage: pks[stageLogin]}}); err != nil {
		return err
	}
	enrol, err := a.ensureFlow(ctx, EnrolmentFlow, map[string]any{"name": "HomersLab: create a passkey from a link",
		"title": "Create your passkey for " + h.Name, "designation": "enrollment", "authentication": "none",
		"denied_action": "message", "policy_engine_mode": "all", "compatibility_mode": false, "layout": "stacked"})
	if err != nil {
		return err
	}
	if err := a.bindStages(ctx, enrol, []stageBinding{
		{Stage: pks[stageLink]},
		{Stage: pks[stageRefused], Policies: []binding{{Policy: person, Negate: true}}},
		{Stage: pks[stageEnrol], Policies: []binding{{Policy: person}}},
		{Stage: pks[stageAfter]},
	}); err != nil {
		return err
	}
	// Nothing but an enrolment link starts the enrolment flow: it needs no other binding, and only invitations
	// HomersLab minted (for this flow, with a person) get past its first stages.
	household, err := a.ensure(ctx, "/policies/expression/", policyHousehold, map[string]any{"name": policyHousehold, "expression": householdPerson})
	if err != nil {
		return err
	}
	if err := a.refuseElsewhere(ctx, signIn, pks[stageNotHere], household); err != nil {
		return err
	}
	return a.ensureBrand(ctx, signIn)
}

// flowBinding is a stage binding of a flow, as the refusal elsewhere reads it.
type flowBinding struct {
	PK     string `json:"pk"`
	PBM    string `json:"policybindingmodel_ptr_id"`
	Stage  string `json:"stage"`
	Order  int    `json:"order"`
	OnPlan bool   `json:"evaluate_on_plan"`
	ReEval bool   `json:"re_evaluate_policies"`
	Obj    struct {
		Model string `json:"meta_model_name"`
	} `json:"stage_obj"`
}

// guarded says whether a stage signs a person in, or sets something up for them: a way to sign in, or their details.
func guarded(model string) bool {
	return model == loginStageModel || model == writeStageModel || strings.HasPrefix(model, authenticatorModels)
}

// loginsElsewhere is every flow but HomersLab's own two that signs a person in or sets something up for them, with
// its stage bindings and the order a refusal must have there: just before the first stage that does, after whatever
// it asks first (so it tells no one who isn't past a password that the person is the household's).
func (a *Authentik) loginsElsewhere(ctx context.Context, signIn string) (map[string][]flowBinding, map[string]int, error) {
	flows, err := list[flow](ctx, a.api, "/flows/instances/")
	if err != nil {
		return nil, nil, err
	}
	bindings, at := map[string][]flowBinding{}, map[string]int{}
	for _, f := range flows {
		if f.PK == signIn || f.Slug == SignInFlow || f.Slug == EnrolmentFlow {
			continue
		}
		bs, err := list[flowBinding](ctx, a.api, "/flows/bindings/?target="+q(f.PK))
		if err != nil {
			return nil, nil, err
		}
		first, login := 0, false
		for _, b := range bs {
			if guarded(b.Obj.Model) && (!login || b.Order < first) {
				first, login = b.Order, true
			}
		}
		if login {
			bindings[f.PK], at[f.PK] = bs, first-1
		}
	}
	return bindings, at, nil
}

// refuseElsewhere puts the refusal for the household's people into every other flow that signs a person in or sets
// something up for them.
func (a *Authentik) refuseElsewhere(ctx context.Context, signIn, refusal, household string) error {
	flows, at, err := a.loginsElsewhere(ctx, signIn)
	if err != nil {
		return err
	}
	for f, bs := range flows {
		kept := ""
		for _, b := range bs {
			if b.Stage != refusal {
				continue
			}
			if kept == "" && b.Order == at[f] && !b.OnPlan && b.ReEval {
				kept = b.PBM
				continue
			}
			if err := a.api.call(ctx, http.MethodDelete, "/flows/bindings/"+b.PK+"/", nil, nil); err != nil {
				return err
			}
		}
		if kept == "" {
			var out struct {
				PBM string `json:"policybindingmodel_ptr_id"`
			}
			if err := a.api.call(ctx, http.MethodPost, "/flows/bindings/", map[string]any{"target": f, "stage": refusal,
				"order": at[f], "evaluate_on_plan": false, "re_evaluate_policies": true, "invalid_response_action": "retry",
				"policy_engine_mode": "all"}, &out); err != nil {
				return err
			}
			kept = out.PBM
		}
		if err := a.bindPolicies(ctx, kept, []binding{{Policy: household}}); err != nil {
			return err
		}
	}
	return nil
}

// refusedElsewhere says whether every other flow that signs a person in or sets something up for them refuses the
// household's people first, with HomersLab's own policy, unchanged.
func (a *Authentik) refusedElsewhere(ctx context.Context) (bool, error) {
	refusal, err1 := one[struct {
		PK string `json:"pk"`
	}](ctx, a.api, "/stages/deny/?name="+q(stageNotHere))
	household, err2 := one[struct {
		PK         string `json:"pk"`
		Expression string `json:"expression"`
	}](ctx, a.api, "/policies/expression/?name="+q(policyHousehold))
	signIn, err3 := a.flow(ctx, SignInFlow)
	if err := errors.Join(err1, err2, err3); err != nil || refusal == nil || household == nil || signIn == nil {
		return false, err
	}
	if strings.TrimSpace(household.Expression) != strings.TrimSpace(householdPerson) { // the API trims it
		return false, nil
	}
	flows, at, err := a.loginsElsewhere(ctx, signIn.PK)
	if err != nil {
		return false, err
	}
	for f, bs := range flows {
		ok := false
		for _, b := range bs {
			if b.Stage != refusal.PK || b.Order != at[f] || b.OnPlan || !b.ReEval {
				continue
			}
			type pb struct {
				Policy string `json:"policy"`
				Negate bool   `json:"negate"`
				On     bool   `json:"enabled"`
			}
			ps, err := list[pb](ctx, a.api, "/policies/bindings/?target="+q(b.PBM))
			if err != nil {
				return false, err
			}
			ok = len(ps) == 1 && ps[0].Policy == household.PK && !ps[0].Negate && ps[0].On
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// ensureBrand makes the brand of the sign-in's own name: the household's name, HomersLab's passkey-only sign-in, and
// no recovery flow.
func (a *Authentik) ensureBrand(ctx context.Context, signIn string) error {
	type brand struct {
		UUID   string `json:"brand_uuid"`
		Domain string `json:"domain"`
	}
	type brandFlow struct {
		UUID   string  `json:"brand_uuid"`
		Domain string  `json:"domain"`
		SignIn *string `json:"flow_authentication"`
	}
	all, err := list[brandFlow](ctx, a.api, "/core/brands/")
	if err != nil {
		return err
	}
	// The brand of the sign-in's name, or else the one serving HomersLab's sign-in under another name; any other that
	// serves it goes, so the sign-in has one name.
	var have *brand
	for _, b := range all {
		if b.Domain == a.h.SignIn() {
			have = &brand{UUID: b.UUID, Domain: b.Domain}
		}
	}
	for _, b := range all {
		if b.Domain == a.h.SignIn() || b.SignIn == nil || *b.SignIn != signIn {
			continue
		}
		if have == nil {
			have = &brand{UUID: b.UUID, Domain: b.Domain}
			continue
		}
		if err := a.api.call(ctx, http.MethodDelete, "/core/brands/"+b.UUID+"/", nil, nil); err != nil {
			return err
		}
	}
	body := map[string]any{"domain": a.h.SignIn(), "default": false, "branding_title": a.h.Name, "flow_authentication": signIn,
		"flow_recovery": nil, "flow_unenrollment": nil}
	if inv, err := a.flow(ctx, "default-invalidation-flow"); err == nil && inv != nil {
		body["flow_invalidation"] = inv.PK
	}
	if have == nil {
		return a.api.call(ctx, http.MethodPost, "/core/brands/", body, nil)
	}
	return a.api.call(ctx, http.MethodPatch, "/core/brands/"+have.UUID+"/", body, nil)
}

func (a *Authentik) person(ctx context.Context, username string) (*user, error) {
	u, err := one[user](ctx, a.api, "/core/users/?username="+q(username))
	if err != nil {
		return nil, err
	}
	if u == nil || u.Path != userPath || u.Type != "internal" {
		return nil, idp.ErrNoPerson
	}
	return u, nil
}

// EnsurePerson makes a person, with no password, in their role's group and no other role's.
func (a *Authentik) EnsurePerson(ctx context.Context, p idp.Person) error {
	if p.Username == "" || !slices.Contains(idp.Roles(), p.Role) {
		return fmt.Errorf("authentik: a person needs a username and a role")
	}
	groups := map[string]string{}
	for _, r := range idp.Roles() {
		g, err := one[group](ctx, a.api, "/core/groups/?name="+q(idp.RoleGroup(r)))
		if err != nil {
			return err
		}
		if g == nil {
			return errors.New("authentik: the groups for roles are missing: prepare the sign-in first")
		}
		groups[g.PK] = idp.RoleGroup(r)
	}
	want := ""
	for id, name := range groups {
		if name == idp.RoleGroup(p.Role) {
			want = id
		}
	}
	u, err := one[user](ctx, a.api, "/core/users/?username="+q(p.Username))
	if err != nil {
		return err
	}
	if u != nil && (u.Path != userPath || u.Type != "internal") {
		return fmt.Errorf("authentik: the username %q is taken by an account that isn't the household's", p.Username)
	}
	body := map[string]any{"username": p.Username, "name": p.Name, "email": p.Mail, "is_active": true, "type": "internal", "path": userPath}
	if u == nil {
		body["groups"] = []string{want}
		return a.api.call(ctx, http.MethodPost, "/core/users/", body, nil)
	}
	keep := []string{want}
	for _, g := range u.Groups {
		if _, role := groups[g]; !role {
			keep = append(keep, g)
		}
	}
	body["groups"] = keep
	return a.api.call(ctx, http.MethodPatch, fmt.Sprintf("/core/users/%d/", u.PK), body, nil)
}

// People are the household's people in a role.
func (a *Authentik) People(ctx context.Context, r idp.Role) ([]idp.Person, error) {
	us, err := list[user](ctx, a.api, "/core/users/?is_active=true&path="+q(userPath)+"&groups_by_name="+q(idp.RoleGroup(r)))
	if err != nil {
		return nil, err
	}
	out := make([]idp.Person, 0, len(us))
	for _, u := range us {
		if u.Type == "internal" {
			out = append(out, idp.Person{Username: u.Username, Name: u.Name, Mail: u.Email, Role: r})
		}
	}
	return out, nil
}

// Passkeys are a person's passkeys.
func (a *Authentik) Passkeys(ctx context.Context, username string) ([]idp.Passkey, error) {
	u, err := a.person(ctx, username)
	if err != nil {
		return nil, err
	}
	type device struct {
		PK      json.RawMessage `json:"pk"`
		Name    string          `json:"name"`
		Created time.Time       `json:"created_on"`
		User    struct {
			PK int `json:"pk"`
		} `json:"user"`
	}
	// The admin list has no filter by person: every passkey is read, and the person's kept.
	ds, err := list[device](ctx, a.api, "/authenticators/admin/webauthn/")
	if err != nil {
		return nil, err
	}
	out := []idp.Passkey{}
	for _, d := range ds {
		if d.User.PK == u.PK {
			out = append(out, idp.Passkey{ID: pk(d.PK), Name: d.Name, Created: d.Created})
		}
	}
	return out, nil
}

// EnrolmentLink mints an invitation for HomersLab's enrolment flow, for one person, single use, for
// idp.EnrolmentLife. A new link ends the person's earlier ones.
func (a *Authentik) EnrolmentLink(ctx context.Context, username string) (idp.Link, error) {
	u, err := a.person(ctx, username)
	if err != nil {
		return idp.Link{}, err
	}
	if !u.IsActive {
		return idp.Link{}, idp.ErrNoPerson
	}
	f, err := a.flow(ctx, EnrolmentFlow)
	if err != nil {
		return idp.Link{}, err
	}
	if f == nil {
		return idp.Link{}, errors.New("authentik: the enrolment flow is missing: prepare the sign-in first")
	}
	type invitation struct {
		PK        string         `json:"pk"`
		Name      string         `json:"name"`
		FixedData map[string]any `json:"fixed_data"`
	}
	old, err := list[invitation](ctx, a.api, "/stages/invitation/invitations/?flow__slug="+q(EnrolmentFlow))
	if err != nil {
		return idp.Link{}, err
	}
	for _, o := range old {
		if o.FixedData[personField] == username || strings.HasPrefix(o.Name, enrolmentPrefix+username+"-") {
			if err := a.api.call(ctx, http.MethodDelete, "/stages/invitation/invitations/"+o.PK+"/", nil, nil); err != nil && !notFound(err) {
				return idp.Link{}, err
			}
		}
	}
	expires := time.Now().Add(idp.EnrolmentLife).UTC()
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	var inv invitation
	if err := a.api.call(ctx, http.MethodPost, "/stages/invitation/invitations/", map[string]any{
		"name":       enrolmentPrefix + username + "-" + fmt.Sprintf("%x", suffix),
		"expires":    expires.Format(time.RFC3339),
		"single_use": true,
		"flow":       f.PK,
		"fixed_data": map[string]string{personField: username},
	}, &inv); err != nil {
		return idp.Link{}, err
	}
	return idp.Link{URL: idp.Secret(a.h.URL("/if/flow/" + EnrolmentFlow + "/?itoken=" + inv.PK)), ExpiresAt: expires}, nil
}

type redirectURI struct {
	Mode string `json:"matching_mode"`
	URL  string `json:"url"`
}

// EnsureClient makes an app's confidential OIDC client and its application, which only the client's roles may open.
func (a *Authentik) EnsureClient(ctx context.Context, c idp.Client) (idp.ClientCredentials, error) {
	if !strings.HasPrefix(c.Slug, "homerslab-") || len(c.RedirectURIs) == 0 {
		return idp.ClientCredentials{}, errors.New("authentik: a client needs a homerslab- slug and a redirect address")
	}
	consent, err := a.flow(ctx, consentFlow)
	if err != nil {
		return idp.ClientCredentials{}, err
	}
	inval, err := a.flow(ctx, invalidateFlow)
	if err != nil {
		return idp.ClientCredentials{}, err
	}
	if consent == nil || inval == nil {
		return idp.ClientCredentials{}, errors.New("authentik: its default provider flows are missing")
	}
	type mapping struct {
		PK      string `json:"pk"`
		Managed string `json:"managed"`
	}
	maps, err := list[mapping](ctx, a.api, "/propertymappings/provider/scope/")
	if err != nil {
		return idp.ClientCredentials{}, err
	}
	var scopes []string
	for _, m := range maps {
		switch m.Managed {
		case "goauthentik.io/providers/oauth2/scope-openid", "goauthentik.io/providers/oauth2/scope-email", "goauthentik.io/providers/oauth2/scope-profile":
			scopes = append(scopes, m.PK)
		}
	}
	if len(scopes) != 3 {
		return idp.ClientCredentials{}, errors.New("authentik: its OpenID scope mappings are missing")
	}
	cert, err := one[struct {
		PK string `json:"pk"`
	}](ctx, a.api, "/crypto/certificatekeypairs/?has_key=true&name="+q(selfSignedCert))
	if err != nil || cert == nil {
		return idp.ClientCredentials{}, fmt.Errorf("authentik: its signing key is missing: %v", err)
	}
	uris := make([]redirectURI, len(c.RedirectURIs))
	for i, u := range c.RedirectURIs {
		uris[i] = redirectURI{Mode: "strict", URL: u}
	}
	body := map[string]any{"name": c.Slug, "authorization_flow": consent.PK, "invalidation_flow": inval.PK,
		"client_type": "confidential", "redirect_uris": uris, "property_mappings": scopes, "signing_key": cert.PK,
		"sub_mode": "hashed_user_id", "issuer_mode": "per_provider", "include_claims_in_id_token": true,
		"access_code_validity": "minutes=1", "access_token_validity": "minutes=5", "refresh_token_validity": "hours=12",
		"grant_types": []string{"authorization_code"}, "authentication_flow": nil}
	if c.PasskeyOnly {
		f, err := a.flow(ctx, SignInFlow)
		if err != nil || f == nil {
			return idp.ClientCredentials{}, errors.New("authentik: the passkey-only sign-in is missing: prepare the sign-in first")
		}
		body["authentication_flow"] = f.PK
	}
	type provider struct {
		PK     int    `json:"pk"`
		ID     string `json:"client_id"`
		Secret string `json:"client_secret"`
	}
	p, err := one[provider](ctx, a.api, "/providers/oauth2/?name="+q(c.Slug))
	if err != nil {
		return idp.ClientCredentials{}, err
	}
	if p == nil {
		p = &provider{}
		err = a.api.call(ctx, http.MethodPost, "/providers/oauth2/", body, p)
	} else {
		err = a.api.call(ctx, http.MethodPatch, fmt.Sprintf("/providers/oauth2/%d/", p.PK), body, p)
	}
	if err != nil {
		return idp.ClientCredentials{}, err
	}
	app := map[string]any{"name": c.Name, "slug": c.Slug, "provider": p.PK, "policy_engine_mode": "any", "meta_hide": false}
	var have struct {
		Slug string `json:"slug"`
	}
	err = a.api.call(ctx, http.MethodGet, "/core/applications/"+c.Slug+"/", nil, &have)
	switch {
	case notFound(err):
		err = a.api.call(ctx, http.MethodPost, "/core/applications/", app, nil)
	case err == nil:
		err = a.api.call(ctx, http.MethodPatch, "/core/applications/"+c.Slug+"/", app, nil)
	}
	if err != nil {
		return idp.ClientCredentials{}, err
	}
	var full struct {
		PK string `json:"pbm_uuid"`
	}
	if err := a.api.call(ctx, http.MethodGet, "/core/applications/"+c.Slug+"/", nil, &full); err != nil {
		return idp.ClientCredentials{}, err
	}
	var bs []binding
	for _, r := range c.Roles {
		g, err := one[group](ctx, a.api, "/core/groups/?name="+q(idp.RoleGroup(r)))
		if err != nil || g == nil {
			return idp.ClientCredentials{}, fmt.Errorf("authentik: the group of %s is missing: %v", r, err)
		}
		bs = append(bs, binding{Group: g.PK})
	}
	if len(bs) == 0 {
		bs = []binding{{Policy: a.neverPK(ctx)}} // nobody: an application with no binding is open to everyone
	}
	if err := a.bindPolicies(ctx, full.PK, bs); err != nil {
		return idp.ClientCredentials{}, err
	}
	return idp.ClientCredentials{ID: p.ID, Secret: idp.Secret(p.Secret), Issuer: a.h.URL("/application/o/" + c.Slug + "/")}, nil
}

func (a *Authentik) neverPK(ctx context.Context) string {
	id, _ := a.ensure(ctx, "/policies/expression/", policyNever, map[string]any{"name": policyNever,
		"expression": "# HomersLab: this flow applies to nobody.\nreturn False\n"})
	return id
}

// Report says what Authentik can do, as HomersLab finds it now.
func (a *Authentik) Report(ctx context.Context) (idp.Report, error) {
	var v struct {
		Current string `json:"version_current"`
	}
	if err := a.api.call(ctx, http.MethodGet, "/admin/version/", nil, &v); err != nil {
		return idp.Report{}, err
	}
	r := idp.Report{Provider: "authentik", Version: v.Current, OIDC: true, ForwardAuth: true, LDAP: true}
	// Authentik binds a passkey to the name a flow is served under, and checks the origin it is served at: for
	// HomersLab's sign-in, that is the domain of the brand that serves it. The port is the edge's.
	if f, err := a.flow(ctx, SignInFlow); err != nil {
		return idp.Report{}, err
	} else if f != nil {
		type brand struct {
			Domain string  `json:"domain"`
			SignIn *string `json:"flow_authentication"`
		}
		bs, err := list[brand](ctx, a.api, "/core/brands/")
		if err != nil {
			return idp.Report{}, err
		}
		var names []string
		for _, b := range bs {
			if b.SignIn != nil && *b.SignIn == f.PK {
				names = append(names, b.Domain)
			}
		}
		if len(names) == 1 {
			r.RPID = names[0]
			r.Origin = "https://" + names[0] + strings.TrimPrefix(a.h.Origin(), "https://"+a.h.SignIn()) // and the edge's port
		}
	}
	type webauthnSetup struct {
		UV string `json:"user_verification"`
	}
	type validate struct {
		UV      string   `json:"webauthn_user_verification"`
		Classes []string `json:"device_classes"`
		NotConf string   `json:"not_configured_action"`
	}
	type identify struct {
		Password *string `json:"password_stage"`
		Passless *string `json:"passwordless_flow"`
	}
	enrol, err1 := one[webauthnSetup](ctx, a.api, "/stages/authenticator/webauthn/?name="+q(stageEnrol))
	check, err2 := one[validate](ctx, a.api, "/stages/authenticator/validate/?name="+q(stagePasskey))
	ident, err3 := one[identify](ctx, a.api, "/stages/identification/?name="+q(stageIdentify))
	if err := errors.Join(err1, err2, err3); err != nil {
		return idp.Report{}, err
	}
	if enrol != nil && check != nil {
		r.UserVerification = check.UV
		if enrol.UV != "required" {
			r.UserVerification = enrol.UV
		}
		r.PasskeyOnlySignIn = ident != nil && ident.Password == nil && ident.Passless == nil &&
			slices.Equal(check.Classes, []string{"webauthn"}) && check.NotConf == "deny"
	}
	if r.PasskeyOnlySignIn {
		only, err := a.refusedElsewhere(ctx)
		if err != nil {
			return idp.Report{}, err
		}
		r.PasskeyOnlySignIn = only
	}
	ak, err := one[user](ctx, a.api, "/core/users/?username="+q(firstAdmin))
	if err != nil {
		return idp.Report{}, err
	}
	f, err := a.flow(ctx, firstAdminFlow)
	if err != nil {
		return idp.Report{}, err
	}
	r.FirstAdminOff = (ak == nil || !ak.IsActive) && (f == nil || f.Authentication == "require_superuser")
	return r, nil
}
