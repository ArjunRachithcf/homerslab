// Package idp is the identity provider contract: what HomersLab needs from Authentik and Kanidm alike (spec: Adapter
// contracts → Identity providers; ADR 0004, amended). Each provider is an adapter in its own package, and every
// adapter passes the same contract suite (idptest) against the real software in containers.
//
// The household account is the one sign-in for everyone. Its passkeys are made at the sign-in's own name,
// auth.<main domain>, which is also their RP ID, never the bare domain (#83). Every passkey requires user
// verification, at enrolment and at every assertion (#84). Admins sign in only through a passkey-only flow, and the
// first admin's passkey comes only from a one-time enrolment link the manager mints (#84, #87).
package idp

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// Role is what a person may do; every role is a group in the provider.
type Role = mesh.Role

// Roles, as the mesh names them.
const (
	Admin = mesh.Admin
	Adult = mesh.Adult
	Kid   = mesh.Kid
)

// Roles are every role, in order.
func Roles() []Role { return []Role{Admin, Adult, Kid} }

// RoleGroup is a role's group in the provider. It carries HomersLab's prefix, so a household's own groups are never
// touched.
func RoleGroup(r Role) string { return "homerslab-" + string(r) + "s" }

// Secret is a credential: it never prints, never marshals and never logs.
type Secret = mesh.Secret

// SignInPrefix is the sign-in's name under the main domain.
const SignInPrefix = "auth."

// Household is the sign-in as the household uses it.
type Household struct {
	// Name is the household's name, which the sign-in's pages show.
	Name string
	// Domain is the main domain; the sign-in answers at auth.<Domain>, at Port (ADR 0021).
	Domain string
	Port   int
}

// SignIn is the sign-in's name, auth.<main domain>: the passkeys' RP ID.
func (h Household) SignIn() string { return SignInPrefix + h.Domain }

// Origin is the sign-in's one origin, https://auth.<main domain>[:port], which WebAuthn checks exactly.
func (h Household) Origin() string {
	o := "https://" + h.SignIn()
	if h.Port != 0 && h.Port != 443 {
		o += ":" + strconv.Itoa(h.Port)
	}
	return o
}

// URL is a path at the sign-in's origin.
func (h Household) URL(path string) string { return h.Origin() + path }

// Person is someone in the household account.
type Person struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Mail     string `json:"mail"`
	Role     Role   `json:"role"`
}

// Passkey is one of a person's passkeys, as the provider lists it. None of it is secret.
type Passkey struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	// RPID is the name the passkey is bound to: always the sign-in's own.
	RPID string `json:"rpId"`
}

// EnrolmentLife is how long an enrolment link works, once (#84, #87).
const EnrolmentLife = 30 * time.Minute

// Link is a one-time enrolment link: its URL is a bearer credential until it is used or expires.
type Link struct {
	URL       Secret
	ExpiresAt time.Time
}

// Client is an app's OIDC client: who may sign in to it, and where the provider sends them back.
type Client struct {
	// Slug names the client in the provider, homerslab-<app>.
	Slug string
	// Name is what the sign-in shows.
	Name         string
	RedirectURIs []string
	// Roles may sign in; nobody else.
	Roles []Role
	// PasskeyOnly signs everyone in through the passkey-only flow (Admin: admins sign in only that way).
	PasskeyOnly bool
}

// Guard is an app's forward-auth guard at the edge: its own, for its one name and its audience (#83: never one for the
// whole domain). The edge asks it before any request reaches the app.
type Guard struct {
	// Slug names the guard in the provider, homerslab-<app>; Name is what the sign-in shows.
	Slug string
	Name string
	// URL is the app's one URL, https://<name>.<domain>[:port] (ADR 0021): the only name the guard answers for.
	URL string
	// Roles may open the app; nobody else.
	Roles []Role
}

// ClientCredentials are what an app needs of its OIDC client. Secret is a credential.
type ClientCredentials struct {
	ID     string
	Secret Secret
	// Issuer is the client's issuer, at the sign-in's origin; its discovery document is Issuer +
	// "/.well-known/openid-configuration" (spec: OIDC Discovery 1.0).
	Issuer string
}

// Report is what a provider can do, as its adapter finds it.
type Report struct {
	Provider string `json:"provider"` // authentik, kanidm
	Version  string `json:"version"`
	// OIDC, ForwardAuth and LDAP are the sign-in methods it provides to apps.
	OIDC        bool `json:"oidc"`
	ForwardAuth bool `json:"forwardAuth"`
	LDAP        bool `json:"ldap"`
	// RPID and Origin are what its passkeys are bound to and checked against: auth.<main domain> and its one URL.
	RPID   string `json:"rpId"`
	Origin string `json:"origin"`
	// UserVerification is what its passkey flows ask of an authenticator, at enrolment and at every assertion.
	UserVerification string `json:"userVerification"`
	// FirstAdminOff says the provider's own first-admin flow applies to nobody.
	FirstAdminOff bool `json:"firstAdminOff"`
	// PasskeyOnlySignIn says admins sign in through a flow that takes a passkey and nothing else.
	PasskeyOnlySignIn bool `json:"passkeyOnlySignIn"`
}

// Provider is one identity provider's API, as HomersLab's own account in it.
type Provider interface {
	// Report says what the provider can do.
	Report(ctx context.Context) (Report, error)
	// Prepare makes the household's sign-in: the groups for roles, HomersLab's passkey-only sign-in flow and its
	// passkey-enrolment flow, the household's name. It corrects what drifted and changes nothing that isn't
	// HomersLab's. It is idempotent.
	Prepare(ctx context.Context, h Household) error
	// EnsurePerson makes a person, or brings their name, mail and role's group in line. They have no password and no
	// passkey: one comes only from an enrolment link.
	EnsurePerson(ctx context.Context, p Person) error
	// People are the people in a role.
	People(ctx context.Context, r Role) ([]Person, error)
	// Passkeys are a person's passkeys.
	Passkeys(ctx context.Context, username string) ([]Passkey, error)
	// EnrolmentLink mints a one-time link that adds a passkey to a person's account at the sign-in's own address. It
	// works once, for EnrolmentLife, and runs HomersLab's own flow with only a passkey-enrolment stage, never the
	// provider's recovery flow.
	EnrolmentLink(ctx context.Context, username string) (Link, error)
	// EnsureClient makes an app's OIDC client, or brings it in line: its redirect addresses exactly the ones given
	// (https, no wildcard, or an app's own private-use scheme), its roles the only ones it opens to. Its id is its slug
	// and its credentials never change once it is made, so a change reaches the app with no new secret.
	EnsureClient(ctx context.Context, c Client) (ClientCredentials, error)
	// DeleteClient deletes an app's OIDC client and what opens it; one that isn't there is gone already.
	DeleteClient(ctx context.Context, slug string) error
	// EnsureGuard makes an app's guard, or brings it in line: only its roles get past it, at its URL only.
	EnsureGuard(ctx context.Context, g Guard) error
}

// Errors an adapter returns, so callers can say what to fix.
var (
	ErrTokenRefused = errors.New("the identity provider refused HomersLab's token")
	ErrUnreachable  = errors.New("the identity provider can't be reached")
	ErrNoPerson     = errors.New("no such person in the household account")
	ErrNotReady     = errors.New("the identity provider isn't ready yet")
)
