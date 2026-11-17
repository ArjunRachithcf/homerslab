// Package authentik is the identity provider adapter for Authentik (ADR 0004): the household account, as Homeward's
// own service account in it, through Authentik's REST API. It passes the identity provider contract suite against a
// real Authentik in containers (contract_test.go).
//
// Homeward owns these objects, named homerslab-*, and corrects them when they drift:
//   - the groups for roles;
//   - the passkey-only sign-in flow: identification without a password, then a passkey with user verification
//     required, then the session. It is the brand's sign-in, and every client's that is passkey-only;
//   - the passkey-enrolment flow, run only from a one-time link: an invitation for one person, then a passkey with
//     user verification required. It is never the brand's recovery flow, and it has no other stage;
//   - the brand of the sign-in's own name;
//   - in every other flow that signs a person in or sets up a way to (Authentik's default authentication flow and
//     its authenticator setup flows among them), a refusal for the household's people before the first stage that
//     does: they sign in with their passkey, through Homeward's flow, and get a passkey only from an enrolment
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
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/idp"
)

// Version is the Authentik that Homeward's packages pin (packaging/sign-in/authentik.compose.yaml).
const Version = "2026.8.5"

// Homeward's objects in Authentik.
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

// Authentik is the household account, as Homeward's service account.
type Authentik struct {
	api *api
	h   idp.Household
	// Landing is where a finished enrolment goes: Admin, for an admin.
	landing string
	// ldapRetire is how long a closed LDAP application and its provider stay off the outpost before they go.
	ldapRetire time.Duration
}

// Open is Authentik at its upstream base (http://<its bridge address>:9000), as the sign-in of household h, with
// Homeward's own token. landing is where a finished enrolment sends the person.
func Open(base string, h idp.Household, token idp.Secret, landing string) (*Authentik, error) {
	a, err := newAPI(base, h, token)
	if err != nil {
		return nil, err
	}
	return &Authentik{api: a, h: h, landing: landing, ldapRetire: ldapRetireAfter}, nil
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

// Bootstrap makes Homeward's own service account and token with Authentik's bootstrap token, and closes Authentik's
// own way to a first admin. Homeward's token goes to keep (the caller seals it) before anything else changes; only
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
	admins, err := find[group](ctx, b, "/core/groups/?name="+q(superuserGroup), "name", superuserGroup)
	if err != nil {
		return "", err
	}
	if admins == nil {
		return "", errors.New("authentik: its superuser group is missing")
	}
	svc, err := find[user](ctx, b, "/core/users/?username="+q(ServiceUser), "username", ServiceUser)
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
		return "", fmt.Errorf("authentik: Homeward's new token doesn't work: %v", err)
	}
	if err := keep(own); err != nil {
		return "", fmt.Errorf("keeping Homeward's new token: %w", err) // the bootstrap token still works: try again
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

// DefaultFlows are the flows Authentik's own default blueprints make, at Version: those Homeward builds on (consent,
// invalidation), closes (initial-setup), and guards (Prepare's refusal goes into every one that signs a person in or
// sets something up for them).
var DefaultFlows = []string{"default-authentication-flow", "default-authenticator-static-setup", "default-authenticator-totp-setup",
	"default-authenticator-webauthn-setup", "default-invalidation-flow", "default-provider-authorization-explicit-consent",
	consentFlow, invalidateFlow, "default-request", "default-source-authentication", "default-source-enrollment",
	"default-source-pre-authentication", "default-user-settings-flow", firstAdminFlow, "default-password-change"}

// retryEvery is how long a failed default blueprint is left before it is applied again.
const retryEvery = 20 * time.Second

// defaultsReady says whether Authentik's worker has made its default objects, which Homeward's own build on or
// guard: every flow of its default blueprints, with every default and system blueprint applied (they come one by one,
// a fresh Authentik answers before they are), and the signing key. Prepare guards the flows it finds, so it must not
// run before they all are there.
func defaultsReady(ctx context.Context, a *api) error {
	for _, slug := range DefaultFlows {
		f, err := find[flow](ctx, a, "/flows/instances/?slug="+q(slug), "slug", slug)
		if err != nil {
			return err
		}
		if f == nil {
			return fmt.Errorf("%w: its default flow %s isn't made yet", idp.ErrNotReady, slug)
		}
	}
	type instance struct {
		PK          string    `json:"pk"`
		Path        string    `json:"path"`
		Status      string    `json:"status"`
		Enabled     bool      `json:"enabled"`
		LastApplied time.Time `json:"last_applied"`
	}
	bs, err := list[instance](ctx, a, "/managed/blueprints/")
	if err != nil {
		return err
	}
	n := 0
	var notYet error
	for _, b := range bs {
		// An orphaned one's file is gone, an upgrade removed it: nothing is left to apply.
		if !b.Enabled || b.Status == "orphaned" || !(strings.HasPrefix(b.Path, "default/") || strings.HasPrefix(b.Path, "system/")) {
			continue
		}
		n++
		if b.Status == "successful" {
			continue
		}
		// One that failed is applied again, as Authentik's worker would only within the hour: at its first start the
		// worker may apply a blueprint before the ones it builds on (#195's CI: default/flow-oobe.yaml). Not more often
		// than every retryEvery, while the caller waits.
		if b.Status == "error" && b.PK != "" && time.Since(b.LastApplied) > retryEvery {
			_ = a.call(ctx, http.MethodPost, "/managed/blueprints/"+b.PK+"/apply/", nil, nil)
		}
		if notYet == nil {
			notYet = fmt.Errorf("%w: its blueprint %s is %s", idp.ErrNotReady, b.Path, b.Status)
		}
	}
	if notYet != nil {
		return notYet
	}
	if n == 0 {
		return fmt.Errorf("%w: it hasn't found its blueprints yet", idp.ErrNotReady)
	}
	cert, err := find[struct {
		PK string `json:"pk"`
	}](ctx, a, "/crypto/certificatekeypairs/?name="+q(selfSignedCert), "name", selfSignedCert)
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
	ak, err := find[user](ctx, a.api, "/core/users/?username="+q(firstAdmin), "username", firstAdmin)
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
	// Every binding must pass ("all"), and the one there is never does: whatever binding appears on the flow later
	// (Authentik applies its blueprint for it again at its worker's start, after an upgrade, hourly), it stays closed.
	if err := a.api.call(ctx, http.MethodPatch, "/flows/instances/"+firstAdminFlow+"/",
		map[string]any{"authentication": "require_superuser", "policy_engine_mode": "all"}, nil); err != nil {
		return err
	}
	never, err := a.ensure(ctx, "/policies/expression/", policyNever, map[string]any{"name": policyNever,
		"expression": "# HomersLab: this flow applies to nobody.\nreturn False\n"})
	if err != nil {
		return err
	}
	return a.bindPolicies(ctx, target{PBM: f.PBM, PK: f.PK}, []binding{{Policy: never}})
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
	PBM              string `json:"policybindingmodel_ptr_id"`
	Slug             string `json:"slug"`
	Designation      string `json:"designation"`
	Authentication   string `json:"authentication"`
	PolicyEngineMode string `json:"policy_engine_mode"`
}

func (a *Authentik) flow(ctx context.Context, slug string) (*flow, error) {
	return find[flow](ctx, a.api, "/flows/instances/?slug="+q(slug), "slug", slug)
}

// ensure makes the object named name at a list endpoint, or brings it in line with body, and returns its pk.
func (a *Authentik) ensure(ctx context.Context, path, name string, body map[string]any) (string, error) {
	type obj struct {
		PK json.RawMessage `json:"pk"`
	}
	have, err := find[obj](ctx, a.api, path+"?name="+q(name), "name", name)
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

// target is what policies are bound to (a flow, a stage binding, an application): as a policy binding model, which
// bindings are made and asked for by, and as itself, its own pk, which is how a binding answers its target.
type target struct{ PBM, PK string }

// bindPolicies makes exactly these policy bindings on a target, in order.
func (a *Authentik) bindPolicies(ctx context.Context, t target, want []binding) error {
	type pb struct {
		PK     string `json:"pk"`
		Target string `json:"target"`
		Policy string `json:"policy"`
		Group  string `json:"group"`
		Negate bool   `json:"negate"`
		Order  int    `json:"order"`
		On     bool   `json:"enabled"`
	}
	have, err := where[pb](ctx, a.api, "/policies/bindings/?target="+q(t.PBM), "target", t.PK)
	if err != nil {
		return err
	}
	// The new bindings come before the old ones go: a target with no binding at all is open to everyone, so it is never
	// left without one, even for a moment (#219). An old binding of the same policy at the same order (which Authentik
	// keeps unique) is changed in place instead.
	kept, gone := make([]bool, len(want)), make([]bool, len(have))
	for j, h := range have {
		i := slices.IndexFunc(want, func(w binding) bool { return w.Policy == h.Policy && w.Group == h.Group && w.Negate == h.Negate })
		if i >= 0 && !kept[i] && h.On && h.Order == i*10 {
			kept[i], gone[j] = true, true
		}
	}
	for i, w := range want {
		if kept[i] {
			continue
		}
		body := map[string]any{"target": t.PBM, "order": i * 10, "enabled": true, "negate": w.Negate, "timeout": 30, "policy": nil, "group": nil}
		if w.Policy != "" {
			body["policy"] = w.Policy
		}
		if w.Group != "" {
			body["group"] = w.Group
		}
		j := slices.IndexFunc(have, func(h pb) bool { return w.Policy != "" && h.Policy == w.Policy && h.Order == i*10 })
		var err error
		if j >= 0 && !gone[j] {
			gone[j] = true
			err = a.api.call(ctx, http.MethodPatch, "/policies/bindings/"+have[j].PK+"/", body, nil)
		} else {
			err = a.api.call(ctx, http.MethodPost, "/policies/bindings/", body, nil)
		}
		if err != nil {
			return err
		}
	}
	for j, h := range have {
		if gone[j] {
			continue
		}
		if err := a.api.call(ctx, http.MethodDelete, "/policies/bindings/"+h.PK+"/", nil, nil); err != nil {
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
	have, err := where[fsb](ctx, a.api, "/flows/bindings/?target="+q(flowPK), "target", flowPK)
	if err != nil {
		return err
	}
	// The wanted stages are in place before anything else goes, so a flow being put back never runs with a stage
	// missing (the LDAP bind flow without its password stage would bind anyone, #204): a binding of the stage that is
	// there already is changed in place, a missing one is made, and only then are the others deleted.
	conforms := func(h fsb, i int) bool { return h.Order == (i+1)*10 && !h.OnPlan && h.ReEval && h.Invalid == "retry" }
	used := make([]bool, len(have))
	for i, w := range want {
		j := -1 // this stage's binding: one that conforms, or else any to change in place
		for k, h := range have {
			if used[k] || h.Stage != w.Stage {
				continue
			}
			if conforms(h, i) {
				j = k
				break
			}
			if j < 0 {
				j = k
			}
		}
		body := map[string]any{"target": flowPK, "stage": w.Stage, "order": (i + 1) * 10, "evaluate_on_plan": false,
			"re_evaluate_policies": true, "invalid_response_action": "retry", "policy_engine_mode": "all"}
		var id target
		refusing := false // the binding holds the stage that refuses until its policies are bound
		switch {
		case j >= 0 && conforms(have[j], i):
			id = target{PBM: have[j].PBM, PK: have[j].PK}
		case j >= 0:
			if err := a.api.call(ctx, http.MethodPatch, "/flows/bindings/"+have[j].PK+"/", body, nil); err != nil {
				return err
			}
			id = target{PBM: have[j].PBM, PK: have[j].PK}
		default:
			// A stage that has policies is never reachable without them (an LDAP app's session stage without its
			// app-password policy would take another app's app password, #204): its new binding first holds the stage
			// that refuses, and becomes the stage's only once its policies are bound.
			if len(w.Policies) > 0 {
				refuse, err := find[struct {
					PK string `json:"pk"`
				}](ctx, a.api, "/stages/deny/?name="+q(stageNotHere), "name", stageNotHere)
				if err != nil || refuse == nil {
					return fmt.Errorf("authentik: the stage that refuses, to bind a stage with policies: %v", err)
				}
				body["stage"], refusing = refuse.PK, true
				// A binding made so before, whose change to the stage never came (a failure or a restart in between), is
				// there still at this order. Authentik keeps a flow's (stage, order) unique, so that one is taken up,
				// still refusing, rather than a second one refused for good (#246's review).
				for k, h := range have {
					if !used[k] && h.Stage == refuse.PK && h.Order == (i+1)*10 {
						j = k
						break
					}
				}
			}
			if j >= 0 {
				if err := a.api.call(ctx, http.MethodPatch, "/flows/bindings/"+have[j].PK+"/", body, nil); err != nil {
					return err
				}
				id = target{PBM: have[j].PBM, PK: have[j].PK}
				break
			}
			var out struct {
				PK  string `json:"pk"`
				PBM string `json:"policybindingmodel_ptr_id"`
			}
			if err := a.api.call(ctx, http.MethodPost, "/flows/bindings/", body, &out); err != nil {
				return err
			}
			id = target{PBM: out.PBM, PK: out.PK}
		}
		if j >= 0 {
			used[j] = true
		}
		if err := a.bindPolicies(ctx, id, w.Policies); err != nil {
			return err
		}
		if refusing {
			body["stage"] = w.Stage
			if err := a.api.call(ctx, http.MethodPatch, "/flows/bindings/"+id.PK+"/", body, nil); err != nil {
				return err
			}
		}
	}
	for j, h := range have {
		if used[j] {
			continue
		}
		if err := a.api.call(ctx, http.MethodDelete, "/flows/bindings/"+h.PK+"/", nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// ensureFlow makes a flow of Homeward's, by slug, or brings it in line.
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
// unexpired invitation for Homeward's enrolment flow, for an active person of the household. Anything else refuses.
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

// householdPerson is the policy that knows the household's people: Homeward's users, and anyone in a role's group.
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
	// Homeward minted (for this flow, with a person) get past its first stages.
	household, err := a.ensure(ctx, "/policies/expression/", policyHousehold, map[string]any{"name": policyHousehold, "expression": householdPerson})
	if err != nil {
		return err
	}
	// The one bind flow of LDAP's first part, which took any app's app password, is no flow of Homeward's now: it goes,
	// before it could count as a login elsewhere (#204, part 2b).
	if err := gone(a.api.call(ctx, http.MethodDelete, "/flows/instances/"+ldapOldFlow+"/", nil, nil)); err != nil {
		return err
	}
	if err := a.refuseElsewhere(ctx, signIn, pks[stageNotHere], household); err != nil {
		return err
	}
	// The LDAP bind flows, once apps have made them, back to their own app's app passwords only, at an outpost only
	// (#204). One app's flow that can't be put back keeps neither the others' nor the rest of Prepare from theirs: its
	// error comes back with theirs, at the end (#246's review).
	objs, err := a.ldapFlows(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, obj := range objs {
		if _, err := a.ldapBindFlow(ctx, obj); err != nil {
			errs = append(errs, fmt.Errorf("authentik: the LDAP bind flow of %s: %w", obj, err))
		}
	}
	// The LDAP outpost back to Homeward's providers alone, each with its own application: orphaned providers are
	// removed.
	if _, err := a.ldapTidy(ctx, true); err != nil {
		errs = append(errs, err)
	}
	// The guards' mappings back to Homeward's (#244).
	if err := a.ensureGuards(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(append(errs, a.ensureBrand(ctx, signIn))...)
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

// loginsElsewhere is every flow but Homeward's own two that signs a person in or sets something up for them, with
// its stage bindings and the order a refusal must have there: just before the first stage that does, after whatever
// it asks first (so it tells no one who isn't past a password that the person is the household's).
func (a *Authentik) loginsElsewhere(ctx context.Context, signIn string) (map[string][]flowBinding, map[string]int, error) {
	flows, err := list[flow](ctx, a.api, "/flows/instances/")
	if err != nil {
		return nil, nil, err
	}
	bindings, at := map[string][]flowBinding{}, map[string]int{}
	for _, f := range flows {
		// The LDAP bind flows take an app password, for an app's LDAP bind, and run only at an outpost (#204); the
		// report checks each.
		if f.PK == signIn || f.Slug == SignInFlow || f.Slug == EnrolmentFlow || ldapFlowSlug.MatchString(f.Slug) {
			continue
		}
		bs, err := where[flowBinding](ctx, a.api, "/flows/bindings/?target="+q(f.PK), "target", f.PK)
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
		kept := target{}
		for _, b := range bs {
			if b.Stage != refusal {
				continue
			}
			if kept.PBM == "" && b.Order == at[f] && !b.OnPlan && b.ReEval {
				kept = target{PBM: b.PBM, PK: b.PK}
				continue
			}
			if err := a.api.call(ctx, http.MethodDelete, "/flows/bindings/"+b.PK+"/", nil, nil); err != nil {
				return err
			}
		}
		if kept.PBM == "" {
			var out struct {
				PK  string `json:"pk"`
				PBM string `json:"policybindingmodel_ptr_id"`
			}
			if err := a.api.call(ctx, http.MethodPost, "/flows/bindings/", map[string]any{"target": f, "stage": refusal,
				"order": at[f], "evaluate_on_plan": false, "re_evaluate_policies": true, "invalid_response_action": "retry",
				"policy_engine_mode": "all"}, &out); err != nil {
				return err
			}
			kept = target{PBM: out.PBM, PK: out.PK}
		}
		if err := a.bindPolicies(ctx, kept, []binding{{Policy: household}}); err != nil {
			return err
		}
	}
	return nil
}

// refusedElsewhere says whether every other flow that signs a person in or sets something up for them refuses the
// household's people first, with Homeward's own policy, unchanged.
func (a *Authentik) refusedElsewhere(ctx context.Context) (bool, error) {
	refusal, err1 := find[struct {
		PK string `json:"pk"`
	}](ctx, a.api, "/stages/deny/?name="+q(stageNotHere), "name", stageNotHere)
	household, err2 := find[struct {
		PK         string `json:"pk"`
		Expression string `json:"expression"`
	}](ctx, a.api, "/policies/expression/?name="+q(policyHousehold), "name", policyHousehold)
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
				Target string `json:"target"`
				Policy string `json:"policy"`
				Negate bool   `json:"negate"`
				On     bool   `json:"enabled"`
			}
			ps, err := where[pb](ctx, a.api, "/policies/bindings/?target="+q(b.PBM), "target", b.PK)
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

// ensureBrand makes the brand of the sign-in's own name: the household's name, Homeward's passkey-only sign-in, and
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
	// The brand of the sign-in's name, or else the one serving Homeward's sign-in under another name; any other that
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
	u, err := find[user](ctx, a.api, "/core/users/?username="+q(username), "username", username)
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
		g, err := find[group](ctx, a.api, "/core/groups/?name="+q(idp.RoleGroup(r)), "name", idp.RoleGroup(r))
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
	u, err := find[user](ctx, a.api, "/core/users/?username="+q(p.Username), "username", p.Username)
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
	g, err := find[group](ctx, a.api, "/core/groups/?name="+q(idp.RoleGroup(r)), "name", idp.RoleGroup(r))
	if err != nil || g == nil {
		return []idp.Person{}, err // no group yet: nobody in the role
	}
	us, err := where[user](ctx, a.api, "/core/users/?is_active=true&path="+q(userPath)+"&groups_by_name="+q(idp.RoleGroup(r)), "path", userPath)
	if err != nil {
		return nil, err
	}
	out := make([]idp.Person, 0, len(us))
	for _, u := range us {
		if u.Type == "internal" && u.IsActive && slices.Contains(u.Groups, g.PK) { // the filters are checked, never trusted (#219)
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

// Credentials are everything a person holds that signs them in: their confirmed authenticator devices, a password,
// app passwords, API tokens and recovery links that haven't expired, and accounts linked at other sources. A password
// once set stays counted: Authentik says when one was set, never whether it still works. A list filter Authentik
// ignored would only add other people's devices, which refuses a step-up rather than allows one.
func (a *Authentik) Credentials(ctx context.Context, username string) (idp.Credentials, error) {
	u, err := a.person(ctx, username)
	if err != nil {
		return idp.Credentials{}, err
	}
	out := idp.Credentials{Others: []string{}}
	add := func(kind string) {
		if !slices.Contains(out.Others, kind) {
			out.Others = append(out.Others, kind)
		}
	}
	// Every kind of device Authentik has, enterprise ones included; the list isn't paginated.
	var ds []struct {
		Type      string `json:"type"`
		Confirmed bool   `json:"confirmed"`
	}
	if err := a.api.call(ctx, http.MethodGet, fmt.Sprintf("/authenticators/admin/all/?user=%d", u.PK), nil, &ds); err != nil {
		return idp.Credentials{}, err
	}
	for _, d := range ds {
		switch {
		case !d.Confirmed: // a setup never finished signs nobody in
		case d.Type == "authentik_stages_authenticator_webauthn.WebAuthnDevice":
			out.Passkeys++
		case d.Type == "authentik_stages_authenticator_totp.TOTPDevice":
			add(idp.CredTOTP)
		case d.Type == "authentik_stages_authenticator_static.StaticDevice":
			add(idp.CredRecoveryCodes)
		default:
			add(strings.TrimPrefix(strings.ToLower(d.Type), "authentik_stages_authenticator_"))
		}
	}
	// A password: Authentik stamps password_change_date at every password set, and at the account's making, which
	// sets none (Homeward's people get an unusable one).
	var self struct {
		Joined  time.Time `json:"date_joined"`
		Changed time.Time `json:"password_change_date"`
	}
	if err := a.api.call(ctx, http.MethodGet, fmt.Sprintf("/core/users/%d/", u.PK), nil, &self); err != nil {
		return idp.Credentials{}, err
	}
	if self.Changed.Sub(self.Joined) > time.Second {
		add(idp.CredPassword)
	}
	type token struct {
		Intent   string     `json:"intent"`
		Expiring bool       `json:"expiring"`
		Expires  *time.Time `json:"expires"`
		User     int        `json:"user"`
	}
	ts, err := list[token](ctx, a.api, "/core/tokens/?user__username="+q(username))
	if err != nil {
		return idp.Credentials{}, err
	}
	for _, t := range ts {
		live := !t.Expiring || t.Expires == nil || t.Expires.After(time.Now())
		if t.User == u.PK && live && (t.Intent == "api" || t.Intent == "app_password" || t.Intent == "recovery") {
			add(idp.CredToken)
		}
	}
	type connection struct {
		User int `json:"user"`
	}
	cs, err := list[connection](ctx, a.api, fmt.Sprintf("/sources/user_connections/all/?user=%d", u.PK))
	if err != nil {
		return idp.Credentials{}, err
	}
	if slices.ContainsFunc(cs, func(c connection) bool { return c.User == u.PK }) {
		add(idp.CredLinkedAccount)
	}
	slices.Sort(out.Others)
	return out, nil
}

// EnrolmentLink mints an invitation for Homeward's enrolment flow, for one person, single use, for
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
	old, err := where[invitation](ctx, a.api, "/stages/invitation/invitations/?flow__slug="+q(EnrolmentFlow), "flow", f.PK)
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

// clientProviderModel is an OIDC client's provider, as Authentik's transactional API names its model.
const clientProviderModel = "authentik_providers_oauth2.oauth2provider"

// validRedirect says whether a client may send a person's code to u: an https address with nothing but a host, a port
// and a path (no person, no query, no fragment, no wildcard or pattern), or one of the app's own apps' addresses, whose
// private-use scheme holds a dot (RFC 8252, 7.1) and no host. Authentik matches it exactly (strict). The catalog's
// loader takes only what this takes.
func validRedirect(u string) bool {
	if u == "" || strings.ContainsAny(u, "*\\ ") || strings.ContainsFunc(u, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
		return false
	}
	p, err := url.Parse(u)
	if err != nil || p.User != nil || p.RawQuery != "" || p.ForceQuery || p.Fragment != "" || p.Opaque != "" || strings.Contains(p.Path, "..") {
		return false
	}
	switch {
	case p.Scheme == "https":
		return p.Hostname() != "" && !strings.ContainsAny(p.Path, "()[]{}|^$+?")
	case p.Scheme == "http" || p.Scheme == "":
		return false
	default:
		return strings.Contains(p.Scheme, ".") && p.Host == ""
	}
}

// newClientSecret is a client's secret: 64 characters, which Homeward makes and Authentik keeps.
func newClientSecret() string {
	b := make([]byte, 48)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// EnsureClient makes an app's confidential OIDC client and its application, which only the client's roles may open
// (#20). A new client is made in one call, Authentik's transactional application API: the provider, the application
// and the policy bindings that limit it to its audience, all or nothing. Its id is its slug, and its secret one
// Homeward makes. An existing one is brought in line in place, its id and secret kept.
func (a *Authentik) EnsureClient(ctx context.Context, c idp.Client) (idp.ClientCredentials, error) {
	if !strings.HasPrefix(c.Slug, "homerslab-") || len(c.RedirectURIs) == 0 || slices.ContainsFunc(c.RedirectURIs, func(u string) bool { return !validRedirect(u) }) {
		return idp.ClientCredentials{}, errors.New("authentik: a client needs a homerslab- slug and exact https redirect addresses")
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
	mappings, err := list[mapping](ctx, a.api, "/propertymappings/provider/scope/")
	if err != nil {
		return idp.ClientCredentials{}, err
	}
	var scopes []string
	for _, m := range mappings {
		switch m.Managed {
		case "goauthentik.io/providers/oauth2/scope-openid", "goauthentik.io/providers/oauth2/scope-email", "goauthentik.io/providers/oauth2/scope-profile":
			scopes = append(scopes, m.PK)
		}
	}
	if len(scopes) != 3 {
		return idp.ClientCredentials{}, errors.New("authentik: its OpenID scope mappings are missing")
	}
	slices.Sort(scopes)
	cert, err := find[struct {
		PK string `json:"pk"`
	}](ctx, a.api, "/crypto/certificatekeypairs/?has_key=true&name="+q(selfSignedCert), "name", selfSignedCert)
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
		PK     int     `json:"pk"`
		ID     string  `json:"client_id"`
		Secret string  `json:"client_secret"`
		App    *string `json:"assigned_application_slug"`
	}
	p, err := find[provider](ctx, a.api, "/providers/oauth2/?name="+q(c.Slug), "name", c.Slug)
	if err != nil {
		return idp.ClientCredentials{}, err
	}
	if p != nil && p.App != nil && *p.App != c.Slug { // another application's provider is never rewritten (#219)
		return idp.ClientCredentials{}, fmt.Errorf("authentik: the provider %s is another application's, refused", c.Slug)
	}
	issuer := a.h.URL("/application/o/" + c.Slug + "/")
	if p == nil {
		var app struct {
			Slug string `json:"slug"`
		}
		switch err := a.api.call(ctx, http.MethodGet, "/core/applications/"+c.Slug+"/", nil, &app); {
		case notFound(err):
			return a.createClient(ctx, c, body, issuer)
		case err != nil:
			return idp.ClientCredentials{}, err
		}
		// An application without its provider, from an older Homeward: its provider is made, and the two put in line.
		p = &provider{}
		if err := a.api.call(ctx, http.MethodPost, "/providers/oauth2/", body, p); err != nil {
			return idp.ClientCredentials{}, err
		}
	} else if err := a.api.call(ctx, http.MethodPatch, fmt.Sprintf("/providers/oauth2/%d/", p.PK), body, p); err != nil {
		return idp.ClientCredentials{}, err
	}
	if err := a.ensureApp(ctx, c.Slug, c.Name, p.PK, c.Roles); err != nil {
		return idp.ClientCredentials{}, err
	}
	return idp.ClientCredentials{ID: p.ID, Secret: idp.Secret(p.Secret), Issuer: issuer}, nil
}

// createClient makes a new client in one call: the provider, its application, and the bindings that open it to its
// roles only. Authentik applies the three as one blueprint, in one transaction.
func (a *Authentik) createClient(ctx context.Context, c idp.Client, provider map[string]any, issuer string) (idp.ClientCredentials, error) {
	audience, err := a.audience(ctx, c.Roles)
	if err != nil {
		return idp.ClientCredentials{}, err
	}
	secret := newClientSecret()
	provider = maps.Clone(provider)
	provider["client_id"], provider["client_secret"] = c.Slug, secret
	bindings := make([]map[string]any, len(audience))
	for i, b := range audience {
		bindings[i] = map[string]any{"order": i * 10, "enabled": true, "negate": b.Negate, "timeout": 30}
		if b.Group != "" {
			bindings[i]["group"] = b.Group
		} else {
			bindings[i]["policy"] = b.Policy
		}
	}
	var out struct {
		Applied bool `json:"applied"`
	}
	// The answer's logs are Authentik's blueprint importer's, which may quote what it was sent: never read or kept.
	if err := a.api.call(ctx, http.MethodPut, "/core/transactional/applications/", map[string]any{
		"app":             map[string]any{"name": c.Name, "slug": c.Slug, "policy_engine_mode": "any", "meta_hide": false},
		"provider_model":  clientProviderModel,
		"provider":        provider,
		"policy_bindings": bindings,
	}, &out); err != nil {
		var ae *APIError
		if errors.As(err, &ae) {
			ae.Detail = "" // a validation error may quote the provider's fields, its secret among them
		}
		return idp.ClientCredentials{}, err
	}
	if !out.Applied {
		return idp.ClientCredentials{}, errors.New("authentik: it didn't make the client")
	}
	return idp.ClientCredentials{ID: c.Slug, Secret: idp.Secret(secret), Issuer: issuer, Created: true}, nil
}

// audience are the bindings that open an application to roles: each role's group, or, for none, the policy that
// refuses everyone (an application with no binding is open to everyone). It only reads, but for that policy where the
// sign-in's preparing didn't make it.
func (a *Authentik) audience(ctx context.Context, roles []idp.Role) ([]binding, error) {
	var bs []binding
	for _, r := range roles {
		g, err := find[group](ctx, a.api, "/core/groups/?name="+q(idp.RoleGroup(r)), "name", idp.RoleGroup(r))
		if err != nil || g == nil {
			return nil, fmt.Errorf("authentik: the group of %s is missing: %v", r, err)
		}
		bs = append(bs, binding{Group: g.PK})
	}
	if len(bs) == 0 {
		type policy struct {
			PK string `json:"pk"`
		}
		never, err := find[policy](ctx, a.api, "/policies/expression/?name="+q(policyNever), "name", policyNever)
		if err != nil {
			return nil, err
		}
		if never == nil { // the sign-in's preparing makes it; only a sign-in without a first-admin flow lacks it
			pk, err := a.neverPK(ctx)
			if err != nil || pk == "" {
				return nil, fmt.Errorf("authentik: the policy that refuses everyone: %v", err)
			}
			never = &policy{PK: pk}
		}
		bs = []binding{{Policy: never.PK}}
	}
	return bs, nil
}

// DeleteClient deletes an app's OIDC client: its application (and with it the bindings that open it), then its
// provider. One that isn't there is gone already.
func (a *Authentik) DeleteClient(ctx context.Context, slug string) error {
	if !strings.HasPrefix(slug, "homerslab-") || slug == adminClient || slug == homeClient {
		return errors.New("authentik: only an app's own client, homerslab-<app>, is deleted")
	}
	// Its provider, checked before anything is deleted: never another application's (#219).
	type provider struct {
		PK  int     `json:"pk"`
		App *string `json:"assigned_application_slug"`
	}
	p, err := find[provider](ctx, a.api, "/providers/oauth2/?name="+q(slug), "name", slug)
	if err != nil {
		return err
	}
	if p != nil && p.App != nil && *p.App != slug {
		return fmt.Errorf("authentik: the provider %s is another application's, refused", slug)
	}
	// Its application, unless it is another kind's (a guard's, of the same slug): only an OIDC client's, or one left
	// without its provider.
	var app struct {
		Provider *struct {
			Model string `json:"meta_model_name"`
		} `json:"provider_obj"`
	}
	switch err := a.api.call(ctx, http.MethodGet, "/core/applications/"+slug+"/", nil, &app); {
	case notFound(err):
	case err != nil:
		return err
	case app.Provider != nil && app.Provider.Model != clientProviderModel:
		return fmt.Errorf("authentik: %s is not an OIDC client's application", slug)
	default:
		if err := a.api.call(ctx, http.MethodDelete, "/core/applications/"+slug+"/", nil, nil); err != nil && !notFound(err) {
			return err
		}
	}
	if p == nil {
		return nil
	}
	if err := a.api.call(ctx, http.MethodDelete, fmt.Sprintf("/providers/oauth2/%d/", p.PK), nil, nil); err != nil && !notFound(err) {
		return err
	}
	return nil
}

// adminClient and homeClient are Admin's and Home's own clients, which no app's removal deletes.
const (
	adminClient = "homerslab-admin"
	homeClient  = "homerslab-home"
)

// embeddedOutpost is Authentik's own outpost, which its server runs: the guards' forward-auth endpoint.
const embeddedOutpost = "goauthentik.io/outposts/embedded"

// RoleHeader is the header of the guard's answer that names the person's role (#236): admin, adult or kid, or empty
// for none. The edge takes the role from it alone, never from X-Authentik-Groups, whose names Authentik joins with "|":
// a group may be named "x|homerslab-admins".
const RoleHeader = "X-Authentik-Homerslab-Role"

// roleMapping is the scope mapping every guard's provider carries, which computes RoleHeader: from the role groups the
// person is in directly, by their exact names, one of them or none. A group below a role's group (which its audience
// counts) gives no role. Its scope sorts after ak_proxy, Authentik's own proxy mapping, which merges the person's and
// their groups' attributes into the same additionalHeaders before it, so an attribute under RoleHeader's own name can't
// stand in for it. An attribute under any other spelling of a header of Authentik's or Homeward's (another case,
// underscores) would: the outpost sets each, and the last one wins, even over X-Authentik-Uid (review round 2). So when
// any is there, the mapping fails closed: it empties each of them and the role, and the edge refuses the person.
const roleMapping = "HomersLab: the person's role at the edge"

func roleExpression() string {
	var roles []string
	for _, r := range idp.Roles() {
		roles = append(roles, fmt.Sprintf("%q: %q", idp.RoleGroup(r), string(r)))
	}
	return strings.Join([]string{
		"# HomersLab (#236): the person's role at the edge, from the role groups they're in directly, by exact name.",
		"# Attributes that would set a header of Authentik's or HomersLab's in another spelling: emptied, and no role.",
		"roles = {" + strings.Join(roles, ", ") + "}",
		`extra = request.user.group_attributes(request).get("additionalHeaders")`,
		"extra = extra if isinstance(extra, dict) else {}",
		`forged = [k for k in extra if str(k).lower().replace("_", "-").startswith(("x-authentik-", "x-homerslab-"))]`,
		`mine = set(user.groups.filter(name__in=list(roles)).values_list("name", flat=True))`,
		`role = roles[mine.pop()] if len(mine) == 1 and not forged else ""`,
		`headers = {k: "" for k in forged}`,
		fmt.Sprintf("headers[%q] = role", RoleHeader),
		`return {"ak_proxy": {"user_attributes": {"additionalHeaders": headers}}}`,
	}, "\n") + "\n"
}

// roleScope is the role mapping's scope, which sorts after ak_proxy's.
const roleScope = "homerslab"

// authentiksOwn is the prefix of the mappings Authentik manages itself, its blueprints putting them back: among them
// the ones it puts on every proxy provider, whatever it is sent (ProxyProvider.set_oauth_defaults). Neither the API
// nor the console sets a mapping's managed id, so no admin's mapping carries one; a later Authentik that adds a
// default of its own adds one of these.
const authentiksOwn = "goauthentik.io/providers/"

func (a *Authentik) ensureRoleMapping(ctx context.Context) (string, error) {
	return a.ensure(ctx, "/propertymappings/provider/scope/", roleMapping, map[string]any{"name": roleMapping,
		"scope_name": roleScope, "description": "HomersLab: the role the edge passes to an app", "expression": roleExpression()})
}

// guardProvider is a proxy provider, as the guards' checks read it.
type guardProvider struct {
	PK       int      `json:"pk"`
	Name     string   `json:"name"`
	Mode     string   `json:"mode"`
	App      *string  `json:"assigned_application_slug"`
	Mappings []string `json:"property_mappings"`
}

// guardMappings reads the guards: every proxy provider that is a homerslab- application's, and which scope mappings
// each may carry besides Homeward's role mapping (Authentik's own for a proxy provider). role is the role mapping, nil
// when there is none or when there are two.
func (a *Authentik) guardMappings(ctx context.Context) (guards []guardProvider, allowed map[string]bool, role *scopeMapping, err error) {
	ps, err := list[guardProvider](ctx, a.api, "/providers/proxy/")
	if err != nil {
		return nil, nil, nil, err
	}
	for _, p := range ps {
		if p.App != nil && strings.HasPrefix(*p.App, "homerslab-") {
			guards = append(guards, p)
		}
	}
	ms, err := list[scopeMapping](ctx, a.api, "/propertymappings/provider/scope/")
	if err != nil {
		return nil, nil, nil, err
	}
	allowed, mine := map[string]bool{}, 0
	for _, m := range ms {
		if m.Managed != nil && strings.HasPrefix(*m.Managed, authentiksOwn) {
			allowed[m.PK] = true
		}
		if m.Name == roleMapping {
			mine++
			role = &m
		}
	}
	if mine != 1 {
		role = nil
	}
	return guards, allowed, role, nil
}

// asOurs says whether a role mapping is Homeward's as it writes it. Authentik trims an expression's spaces at both
// ends when it saves one.
func (m *scopeMapping) asOurs() bool {
	return m != nil && strings.TrimSpace(m.Expression) == strings.TrimSpace(roleExpression()) && m.Scope == roleScope && m.Managed == nil
}

// scopeMapping is a scope mapping, as the guards' checks read it.
type scopeMapping struct {
	PK         string  `json:"pk"`
	Name       string  `json:"name"`
	Managed    *string `json:"managed"`
	Scope      string  `json:"scope_name"`
	Expression string  `json:"expression"`
}

// mappedAsOurs says whether a guard's provider carries Homeward's role mapping and, besides it, Authentik's own
// mappings for a proxy provider alone. Any other mapping (an admin's, in whatever scope) could name the role after
// Homeward's does, since Authentik merges each mapping's answer in its scope's order, and over the one before it
// (#244).
func mappedAsOurs(p guardProvider, allowed map[string]bool, role string) bool {
	if p.Mode != "forward_single" || !slices.Contains(p.Mappings, role) { // #83: never forward_domain
		return false
	}
	for _, m := range p.Mappings {
		if m != role && !allowed[m] {
			return false
		}
	}
	return true
}

// guardsIntact says whether every guard is as Homeward makes it: its own app's provider, in forward_single mode,
// carrying Homeward's role mapping, unchanged, and no mapping beside it but Authentik's own (#244). A homerslab-
// application whose provider is another one isn't intact either; Prepare leaves that one to EnsureGuard for its app.
func (a *Authentik) guardsIntact(ctx context.Context) (bool, error) {
	guards, allowed, role, err := a.guardMappings(ctx)
	if err != nil || len(guards) == 0 {
		return err == nil, err
	}
	if !role.asOurs() {
		return false, nil
	}
	for _, p := range guards {
		if p.Name != *p.App || !mappedAsOurs(p, allowed, role.PK) {
			return false, nil
		}
	}
	return true, nil
}

// ensureGuards puts each guard's mappings back, as EnsureGuard makes them: Homeward's role mapping as it writes it,
// and on each guard's provider that mapping alone beside Authentik's own (#244). A provider that isn't its
// application's own is another's, which only EnsureGuard, for its app, replaces.
func (a *Authentik) ensureGuards(ctx context.Context) error {
	guards, allowed, role, err := a.guardMappings(ctx)
	if err != nil || len(guards) == 0 {
		return err
	}
	if !role.asOurs() {
		pk, err := a.ensureRoleMapping(ctx)
		if err != nil {
			return err
		}
		role = &scopeMapping{PK: pk}
	}
	// One guard that can't be put back keeps none of the others from it: its error comes back with theirs.
	var errs []error
	for _, p := range guards {
		if p.Name != *p.App || mappedAsOurs(p, allowed, role.PK) {
			continue
		}
		if err := a.api.call(ctx, http.MethodPatch, fmt.Sprintf("/providers/proxy/%d/", p.PK), map[string]any{"mode": "forward_single",
			"cookie_domain": "", "property_mappings": []string{role.PK}}, nil); err != nil {
			errs = append(errs, fmt.Errorf("authentik: the guard %s's mappings: %w", p.Name, err))
		}
	}
	return errors.Join(errs...)
}

// EnsureGuard makes an app's guard: a proxy provider of its own in forward_single mode, at the app's one URL, its
// application, which only the guard's roles may open, on Authentik's embedded outpost (#83: never forward_domain). Its
// provider carries Homeward's role mapping beside the ones Authentik gives every proxy provider.
func (a *Authentik) EnsureGuard(ctx context.Context, g idp.Guard) error {
	if u, err := url.Parse(g.URL); !strings.HasPrefix(g.Slug, "homerslab-") || err != nil || u.Scheme != "https" || u.Hostname() == "" ||
		u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.Contains(u.Host, "*") {
		return errors.New("authentik: a guard needs a homerslab- slug and the app's own https:// URL")
	}
	consent, err := a.flow(ctx, consentFlow)
	if err != nil {
		return err
	}
	inval, err := a.flow(ctx, invalidateFlow)
	if err != nil {
		return err
	}
	if consent == nil || inval == nil {
		return errors.New("authentik: its default provider flows are missing")
	}
	role, err := a.ensureRoleMapping(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{"name": g.Slug, "authorization_flow": consent.PK, "invalidation_flow": inval.PK, "mode": "forward_single",
		"external_host": g.URL, "internal_host": "", "basic_auth_enabled": false, "intercept_header_auth": false, "cookie_domain": "",
		"access_token_validity": "hours=12", "authentication_flow": nil, "property_mappings": []string{role}}
	type provider struct {
		PK   int     `json:"pk"`
		Host string  `json:"external_host"`
		App  *string `json:"assigned_application_slug"`
	}
	// Proxy providers take name__iexact, never name (#219): found by name, the guard's own provider must also be this
	// app's application's, or not yet any application's and at the app's URL; otherwise it is another app's guard, which
	// is never rewritten. The app's own provider follows the app to a new URL (a new domain or port).
	p, err := find[provider](ctx, a.api, "/providers/proxy/?name__iexact="+q(g.Slug), "name", g.Slug)
	if err != nil {
		return err
	}
	if p != nil && (p.App != nil && *p.App != g.Slug || p.App == nil && p.Host != g.URL) {
		return fmt.Errorf("authentik: the provider %s isn't this app's guard (at %s), refused", g.Slug, p.Host)
	}
	if p == nil {
		p = &provider{}
		err = a.api.call(ctx, http.MethodPost, "/providers/proxy/", body, p)
	} else {
		err = a.api.call(ctx, http.MethodPatch, fmt.Sprintf("/providers/proxy/%d/", p.PK), body, p)
	}
	if err != nil {
		return err
	}
	if err := a.ensureApp(ctx, g.Slug, g.Name, p.PK, g.Roles); err != nil {
		return err
	}
	type outpost struct {
		PK        string `json:"pk"`
		Providers []int  `json:"providers"`
	}
	o, err := find[outpost](ctx, a.api, "/outposts/instances/?managed__iexact="+q(embeddedOutpost), "managed", embeddedOutpost)
	if err != nil || o == nil {
		return fmt.Errorf("authentik: its embedded outpost is missing: %v", err)
	}
	if slices.Contains(o.Providers, p.PK) {
		return nil
	}
	providers := append(slices.Clone(o.Providers), p.PK)
	slices.Sort(providers)
	return a.api.call(ctx, http.MethodPatch, "/outposts/instances/"+o.PK+"/", map[string]any{"providers": providers}, nil)
}

// ensureApp makes an application for a provider, or brings it in line, which only roles may open.
func (a *Authentik) ensureApp(ctx context.Context, slug, name string, provider int, roles []idp.Role) error {
	app := map[string]any{"name": name, "slug": slug, "provider": provider, "policy_engine_mode": "any", "meta_hide": false}
	var have struct {
		Slug string `json:"slug"`
	}
	err := a.api.call(ctx, http.MethodGet, "/core/applications/"+slug+"/", nil, &have)
	switch {
	case notFound(err):
		err = a.api.call(ctx, http.MethodPost, "/core/applications/", app, nil)
	case err == nil:
		err = a.api.call(ctx, http.MethodPatch, "/core/applications/"+slug+"/", app, nil)
	}
	if err != nil {
		return err
	}
	var full struct {
		PBM string `json:"pbm_uuid"`
		PK  string `json:"pk"`
	}
	if err := a.api.call(ctx, http.MethodGet, "/core/applications/"+slug+"/", nil, &full); err != nil {
		return err
	}
	bs, err := a.audience(ctx, roles)
	if err != nil {
		return err
	}
	return a.bindPolicies(ctx, target{PBM: full.PBM, PK: full.PK}, bs)
}

func (a *Authentik) neverPK(ctx context.Context) (string, error) {
	return a.ensure(ctx, "/policies/expression/", policyNever, map[string]any{"name": policyNever,
		"expression": "# HomersLab: this flow applies to nobody.\nreturn False\n"})
}

// Report says what Authentik can do, as Homeward finds it now.
func (a *Authentik) Report(ctx context.Context) (idp.Report, error) {
	var v struct {
		Current string `json:"version_current"`
	}
	if err := a.api.call(ctx, http.MethodGet, "/admin/version/", nil, &v); err != nil {
		return idp.Report{}, err
	}
	r := idp.Report{Provider: "authentik", Version: v.Current, OIDC: true, ForwardAuth: true, LDAP: true}
	// Authentik binds a passkey to the name a flow is served under, and checks the origin it is served at: for
	// Homeward's sign-in, that is the domain of the brand that serves it. The port is the edge's.
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
	enrol, err1 := find[webauthnSetup](ctx, a.api, "/stages/authenticator/webauthn/?name="+q(stageEnrol), "name", stageEnrol)
	check, err2 := find[validate](ctx, a.api, "/stages/authenticator/validate/?name="+q(stagePasskey), "name", stagePasskey)
	ident, err3 := find[identify](ctx, a.api, "/stages/identification/?name="+q(stageIdentify), "name", stageIdentify)
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
		// Every bind flow of Homeward's as it should be: one that isn't would take another password than its own app's.
		objs, err := a.ldapFlows(ctx)
		if err != nil {
			return idp.Report{}, err
		}
		ldap := true
		for _, obj := range objs {
			ok, _, err := a.ldapBindIntact(ctx, obj)
			if err != nil {
				return idp.Report{}, err
			}
			ldap = ldap && ok
		}
		// A provider the outpost serves without Homeward's application for it would bind people that application
		// never let in.
		tidy, err := a.ldapTidy(ctx, false)
		if err != nil {
			return idp.Report{}, err
		}
		r.PasskeyOnlySignIn = only && ldap && tidy
	}
	ak, err := find[user](ctx, a.api, "/core/users/?username="+q(firstAdmin), "username", firstAdmin)
	if err != nil {
		return idp.Report{}, err
	}
	f, err := a.flow(ctx, firstAdminFlow)
	if err != nil {
		return idp.Report{}, err
	}
	// A guard whose role another mapping could name, or whose role mapping isn't Homeward's, provides no forward-auth
	// the edge can take a role from (#244).
	if r.ForwardAuth, err = a.guardsIntact(ctx); err != nil {
		return idp.Report{}, err
	}
	r.FirstAdminOff = (ak == nil || !ak.IsActive) && (f == nil || f.Authentication == "require_superuser" && f.PolicyEngineMode == "all")
	return r, nil
}
