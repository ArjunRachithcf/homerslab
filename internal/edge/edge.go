// Package edge is a node's edge: Caddy, unprivileged, configured only through its admin API on a Unix socket (spec:
// Modules → Edge; ADR 0006). Render is the whole configuration the agent loads into it; Client is the admin API.
//
// No private key is readable through the edge (#78):
//   - autosave is off (admin.config.persist false), and the agent pushes the whole configuration again whenever the
//     edge starts;
//   - certificates load from files that the edge's unit receives as systemd credentials sealed by the agent, never
//     inline, so GET /config/ never shows a key;
//   - the admin socket sits in the edge's runtime directory, which only the edge's user and the agent may enter.
package edge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"net/textproto"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ArjunRachithcf/homeward/internal/idp"
)

// HSTS is the Strict-Transport-Security every name the edge serves answers with (#64): a year, without
// includeSubDomains or preload, because a household's domain may also carry sites that aren't Homeward's. It replaces
// whatever an upstream sent.
const HSTS = "max-age=31536000"

// Every cookie the edge passes on is Secure (#64): Caddy runs these on each Set-Cookie value, in order. The first takes
// out a Secure attribute the cookie may have, in any case or spacing; the second puts one at its end.
var secureCookie = []struct {
	search, replace string
	re              *regexp.Regexp
}{
	{`(?i);\s*secure\s*(;|$)`, "$1", nil},
	{`$`, "; Secure", nil},
}

func init() {
	for i := range secureCookie {
		secureCookie[i].re = regexp.MustCompile(secureCookie[i].search)
	}
}

// SecureCookie is a Set-Cookie value as the edge passes it on, by Go's regexp as Caddy's are (the request matrix
// checks Caddy itself).
func SecureCookie(v string) string {
	for _, r := range secureCookie {
		v = r.re.ReplaceAllString(v, r.replace)
	}
	return v
}

// Config is what the edge serves on one node.
type Config struct {
	// AdminSocket is Caddy's admin API; Storage is Caddy's data directory, which never holds a key.
	AdminSocket string
	Storage     string
	// Listen are the node's mesh addresses, and Port the household's port (ADR 0021): the edge answers nowhere else.
	Listen []netip.Addr
	Port   int
	// Certificates are the household's wildcard certificates, one per domain, as files.
	Certificates []Certificate
	// Admin is Admin's site, on the primary only.
	Admin *Site
	// SignIn is the sign-in, auth.<main domain>, on the primary only: the identity provider's server.
	SignIn *Site
	// SignInListen is the sign-in listener's own address on this node (ADR 0020), where apps reach the sign-in, on the
	// household's port: it carries the sign-in's route only, with SNI and Host both the sign-in's name. None: no
	// listener (no sign-in on this node yet).
	SignInListen netip.Addr
	// Home is Home's site, home.<main domain>, on the primary only: Home's own process, on its socket (#21).
	Home *Site
	// Apps are this node's apps' sites.
	Apps []App
}

// App is an app's site: its name, proxied to the app's one upstream (Dial: an address on its bridge, which only the
// edge's user and the agent's may open, ADR 0020). Guard is the forward-auth the edge asks before any request reaches
// a guarded app; SignsIn says the app signs people in itself, with its own OIDC client (#20), and has none. An app is
// one or the other, never neither: a guarded app whose guard is missing is never served unguarded.
type App struct {
	Name    string
	Dial    string
	Guard   *Guard
	SignsIn bool
	// Refused are routes of an app that signs people in itself which sign them in some other way (its own password, a
	// default admin's) or change who someone is to it: the edge answers them 403, and the app never sees them. Each is a
	// path, for every method, or a method and a path ("PUT /api/users"); a path is refused with everything below it.
	Refused []string
}

// Guard is an identity provider's forward-auth, as the edge asks it (spec: Modules → Edge).
type Guard struct {
	// Dial is the provider's server; AuthURI the path the edge asks, with the request's method and URI in
	// X-Forwarded-Method and X-Forwarded-Uri; a 2xx answer lets the request on, anything else goes back to the browser
	// (a redirect to the sign-in).
	Dial    string
	AuthURI string
	// Paths are the provider's own paths on the app's name (its callback), proxied to it, never to the app.
	Paths []string
	// Person, Name and Username are the headers of the provider's 2xx answer that say who someone is: a stable id that
	// survives a rename, and the name to show (Username when Name is empty). Role names their role itself, exactly admin,
	// adult or kid, as the provider computes it from exact group names; a provider that can't gives Groups instead, their
	// groups separated by GroupSep, which no group's name may hold. The edge turns them into the identity contract
	// (Contract); none of them reaches the app.
	Person, Name, Username, Role, Groups, GroupSep string
	// Cookie is the prefix of the provider's own cookies on the app's name, which the app never sees (#83).
	Cookie string
}

// AuthentikGuard is Authentik's embedded outpost, on its server, as the guard of an app with its own proxy provider in
// forward_single mode (#83: never one for the whole domain). X-Authentik-Uid is the person's uid, a hash of their
// primary key that a rename doesn't change. The role is Homeward's own header, which the guard's provider computes
// (authentik.RoleHeader): X-Authentik-Groups joins names with |, which a group's own name may hold.
func AuthentikGuard(dial string) *Guard {
	return &Guard{Dial: dial, AuthURI: "/outpost.goauthentik.io/auth/caddy", Paths: []string{"/outpost.goauthentik.io/*"},
		Person: "X-Authentik-Uid", Name: "X-Authentik-Name", Username: "X-Authentik-Username", Role: "X-Authentik-Homerslab-Role",
		Cookie: "authentik_proxy_"}
}

// KanidmGuard is oauth2-proxy in front of Kanidm, one per app, answering with --set-xauthrequest (research:
// 2026-11-09-spike-kanidm-forward-auth): X-Auth-Request-User is the person's sub, their UUID in Kanidm; oauth2-proxy
// names no display name, so the name is their preferred username; X-Auth-Request-Groups joins their groups with commas,
// by their short names (scope groups_name), and Kanidm refuses a comma in a name (TestKanidmsGroupNamesHoldNoComma).
// oauth2-proxy must never run with --skip-jwt-bearer-tokens: a bearer token would stand in for the session cookie.
func KanidmGuard(dial string) *Guard {
	return &Guard{Dial: dial, AuthURI: "/oauth2/auth", Paths: []string{"/oauth2/*"},
		Person: "X-Auth-Request-User", Name: "X-Auth-Request-Preferred-Username", Username: "X-Auth-Request-Preferred-Username", Groups: "X-Auth-Request-Groups", GroupSep: ",",
		Cookie: "_oauth2_proxy"}
}

// Contract is the identity contract the edge keeps for every guarded app (#236): the app learns who someone is from
// PersonHeader (the one to key people on), NameHeader (text the person may change: never split nor authorise on it) and
// RoleHeader alone, which the edge sets from its guard's answer, the same whichever
// provider the household runs. NameHeader is their name, or their username when the provider has none. RoleHeader is
// admin, adult or kid, from the one role group the person is in; a person in none of them, or in more than one, or
// without an id or any name, is refused, never taken for an adult. Every site deletes every
// header a client sends under Homeward's prefix, in any case or spelling (OwnHeaders), so only the edge's reach an app.
// An app's manifest declares that it relies on it (identity.edge).
const (
	Contract     = "homerslab-v1"
	PersonHeader = "X-HomersLab-Person"
	NameHeader   = "X-HomersLab-Name"
	RoleHeader   = "X-HomersLab-Role"
)

// OwnHeaders are the request headers under Homeward's prefix, which only the edge sets.
var OwnHeaders = []string{"X-HomersLab-*"}

// IdentityHeaders are the request headers that name a person to an app, the providers' and those other providers and
// apps read: a client could forge any of them, so the edge deletes them all from every request to an app before
// anything else, and none reaches an app again (the guard's answer reaches it as the identity contract).
var IdentityHeaders = []string{"X-Authentik-*", "Remote-*", "X-Remote-*", "X-Forwarded-User", "X-Forwarded-Email",
	"X-Forwarded-Groups", "X-Forwarded-Preferred-Username", "X-Forwarded-Access-Token", "X-Auth-Request-*", "X-Webauth-*",
	"X-Token-User-*", "X-User", "X-Email", "X-Username"}

// spellings are the names a client can send for each header, as Caddy's delete compares them: in the form Go's server
// gives every name it reads (textproto's canonical one, for HTTP/1 and HTTP/2 alike), with each dash also as an
// underscore or a dot, which Go keeps but an app behind CGI, WSGI or PHP may read as a dash. Caddy compares a name
// ending in * by its prefix, exactly; any other by its canonical form.
func spellings(names []string) []string {
	var out []string
	for _, n := range names {
		wild := strings.HasSuffix(n, "*")
		parts := strings.Split(strings.TrimSuffix(n, "*"), "-")
		spelt := []string{parts[0]}
		for _, p := range parts[1:] {
			var next []string
			for _, s := range spelt {
				for _, sep := range []string{"-", "_", "."} {
					next = append(next, s+sep+p)
				}
			}
			spelt = next
		}
		for _, v := range spelt {
			v = textproto.CanonicalMIMEHeaderKey(v)
			if wild {
				v += "*"
			}
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// ownSpellings and identitySpellings are OwnHeaders and IdentityHeaders as the edge deletes them.
var ownSpellings, identitySpellings = spellings(OwnHeaders), spellings(IdentityHeaders)

// Certificate is one domain's wildcard certificate, loaded from files.
type Certificate struct {
	Domain            string
	CertFile, KeyFile string
}

// Site is one name the edge serves, proxied to a Unix socket (Upstream) or a TCP address on a bridge (Dial).
type Site struct {
	Name     string
	Upstream string
	Dial     string
}

// FirstAdminPaths are Authentik's own first-admin flow, its page and its API, which the edge refuses on every
// listener, before the provider sees them (#84). Caddy matches paths case-insensitively and after unescaping.
var FirstAdminPaths = []string{
	"/if/flow/initial-setup", "/if/flow/initial-setup/*",
	"/api/v3/flows/executor/initial-setup", "/api/v3/flows/executor/initial-setup/*",
	"/api/v3/flows/instances/initial-setup", "/api/v3/flows/instances/initial-setup/*",
}

// AdminListen is Caddy's admin address for a socket: group members (the agent) may connect, nobody else.
func AdminListen(socket string) string { return "unix/" + socket + "|0660" }

// Render is the edge's whole configuration, as Caddy's JSON. It is deterministic, so it can be compared with a
// golden render and with what the edge reports.
func Render(c Config) ([]byte, error) {
	if c.AdminSocket == "" || c.Storage == "" {
		return nil, errors.New("the edge needs its admin socket and its storage directory")
	}
	if len(c.Listen) == 0 || c.Port <= 0 || c.Port > 65535 || c.Port == 80 {
		return nil, errors.New("the edge needs the node's mesh addresses and the household's port, which is never 80")
	}
	at := func(port int) []string {
		listen := make([]string, 0, len(c.Listen))
		for _, a := range c.Listen {
			listen = append(listen, netip.AddrPortFrom(a.Unmap(), uint16(port)).String())
		}
		slices.Sort(listen)
		return listen
	}
	for _, a := range c.Listen {
		if !a.IsValid() || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() {
			return nil, fmt.Errorf("the edge listens on the node's mesh addresses only, not %v", a)
		}
	}

	var loads []obj
	var policies []obj
	for _, crt := range c.Certificates {
		if crt.Domain == "" || crt.CertFile == "" || crt.KeyFile == "" {
			return nil, errors.New("a certificate needs its domain and both files")
		}
		loads = append(loads, obj{"certificate": crt.CertFile, "key": crt.KeyFile, "tags": []string{crt.Domain}})
		policies = append(policies, obj{
			"match":                 obj{"sni": []string{"*." + crt.Domain}},
			"certificate_selection": obj{"any_tag": []string{crt.Domain}},
		})
	}

	// The names the edge serves, each with what answers it: Admin on its socket; the sign-in on its bridge address,
	// with Authentik's first-admin flow refused before the provider sees it (#84).
	type served struct {
		name   string
		handle obj
	}
	var sites []served
	if c.Admin != nil {
		sites = append(sites, served{c.Admin.Name, obj{"handler": "reverse_proxy", "upstreams": []obj{{"dial": "unix/" + c.Admin.Upstream}}}})
	}
	if c.Home != nil {
		if c.Home.Name == "" || c.Home.Upstream == "" {
			return nil, errors.New("Home needs its name and its socket")
		}
		sites = append(sites, served{c.Home.Name, obj{"handler": "reverse_proxy", "upstreams": []obj{{"dial": "unix/" + c.Home.Upstream}}}})
	}
	if c.SignIn != nil {
		if _, err := netip.ParseAddrPort(c.SignIn.Dial); err != nil || c.SignIn.Name == "" {
			return nil, errors.New("the sign-in needs its name and its upstream's address and port")
		}
		sites = append(sites, served{c.SignIn.Name, obj{"handler": "subroute", "routes": []obj{
			{"match": []obj{{"path": FirstAdminPaths}}, "handle": []obj{{"handler": "static_response", "status_code": 403}}, "terminal": true},
			{"handle": []obj{{"handler": "reverse_proxy", "upstreams": []obj{{"dial": c.SignIn.Dial}}}}},
		}}})
	}
	domains := make([]string, 0, len(c.Certificates))
	for _, crt := range c.Certificates {
		domains = append(domains, crt.Domain)
	}
	for _, app := range c.Apps {
		h, err := appRoute(app, domains)
		if err != nil {
			return nil, err
		}
		sites = append(sites, served{app.Name, h})
	}
	// Each name the edge serves: Homeward's own headers deleted from the request, whatever the site (#236); HSTS and
	// Secure cookies on whatever it answers, set as the answer is written so they replace the upstream's, then what
	// answers it. On port 80, a redirect to HTTPS on the same name, and nothing else.
	var cookies []obj
	for _, r := range secureCookie {
		cookies = append(cookies, obj{"search_regexp": r.search, "replace": r.replace})
	}
	var routes, redirects []obj
	var signIn obj // the sign-in's route, which the sign-in listener carries too
	for _, site := range sites {
		route := obj{
			"match": []obj{{"host": []string{site.name}}},
			"handle": []obj{
				{"handler": "headers", "request": obj{"delete": ownSpellings}, "response": obj{
					"set":      obj{"Strict-Transport-Security": []string{HSTS}},
					"replace":  obj{"Set-Cookie": cookies},
					"deferred": true,
				}},
				site.handle,
			},
			"terminal": true,
		}
		routes = append(routes, route)
		if c.SignIn != nil && site.name == c.SignIn.Name {
			signIn = route
		}
		// Only an origin-form target, a path: an opaque absolute-form one (`GET http:@evil.test`) has none, and would
		// otherwise follow the name in the Location, making it another host's.
		redirects = append(redirects, obj{
			"match": []obj{{"host": []string{site.name}, "path": []string{"/*"}}},
			"handle": []obj{{"handler": "static_response", "status_code": 308,
				"headers": obj{"Location": []string{"https://" + hostPort(site.name, c.Port) + "{http.request.uri}"}}}},
			"terminal": true,
		})
	}
	// Any other name: 421 Misdirected Request, never Caddy's empty 200 for a request no route matches (#81), and never
	// a redirect to a name that isn't the edge's.
	misdirected := obj{"handle": []obj{{"handler": "static_response", "status_code": 421}}}
	routes, redirects = append(routes, misdirected), append(redirects, misdirected)

	var signInServer obj
	if c.SignInListen.IsValid() {
		a := c.SignInListen.Unmap()
		if c.SignIn == nil || !a.Is4() || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() || slices.Contains(c.Listen, a) {
			return nil, fmt.Errorf("the sign-in listener needs the sign-in, and an IPv4 address of its own, not %v", a)
		}
		// The sign-in's name only: a handshake for any other name finds no certificate, and another Host gets 421.
		// The one wildcard certificate that covers the name: *.<the name's parent>.
		_, parent, _ := strings.Cut(c.SignIn.Name, ".")
		var tags []string
		for _, crt := range c.Certificates {
			if strings.EqualFold(crt.Domain, parent) && tags == nil {
				tags = []string{crt.Domain}
			}
		}
		if tags == nil && len(c.Certificates) > 0 {
			return nil, fmt.Errorf("the sign-in listener: no certificate covers %s", c.SignIn.Name)
		}
		if tags != nil {
			signInServer = obj{
				"listen":          []string{netip.AddrPortFrom(a, uint16(c.Port)).String()},
				"protocols":       []string{"h1", "h2"},
				"automatic_https": obj{"disable": true},
				"tls_connection_policies": []obj{{
					"match":                 obj{"sni": []string{c.SignIn.Name}},
					"certificate_selection": obj{"any_tag": tags},
				}},
				"routes": []obj{signIn, misdirected},
			}
		}
	}

	cfg := obj{
		"admin":   obj{"listen": AdminListen(c.AdminSocket), "config": obj{"persist": false}},
		"storage": obj{"module": "file_system", "root": c.Storage},
	}
	// Without a certificate the edge listens nowhere: Caddy would serve plain HTTP on a server without TLS.
	if loads != nil {
		servers := obj{
			"mesh": obj{
				"listen":                  at(c.Port),
				"protocols":               []string{"h1", "h2"},
				"automatic_https":         obj{"disable": true},
				"tls_connection_policies": policies,
				"routes":                  routes,
			},
			// Plain HTTP only redirects to HTTPS (#64): it proxies nothing.
			"redirect": obj{
				"listen":          at(80),
				"protocols":       []string{"h1"},
				"automatic_https": obj{"disable": true},
				"routes":          redirects,
			},
		}
		// Apps reach the sign-in here, over HTTPS only: no plain listener at its address.
		if signInServer != nil {
			servers["sign-in"] = signInServer
		}
		cfg["apps"] = obj{
			"tls":  obj{"certificates": obj{"load_files": loads}, "disable_ocsp_stapling": true},
			"http": obj{"servers": servers},
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// appRoute is a guarded app's site: the client's identity headers deleted first; the provider's own paths to the
// provider; then the guard, whose 2xx answer sets the identity it names (and passes on the provider's refreshed cookie,
// ADR 0006); then the app, without the provider's cookie, and without any cookie it sets for a whole household domain
// (#83). It is the site's handler, which Render serves at the app's name with HSTS and Secure cookies, as every name.
func appRoute(a App, domains []string) (obj, error) {
	if a.SignsIn {
		return nativeRoute(a, domains)
	}
	g := a.Guard
	if a.Name == "" || !validDial(a.Dial) || g == nil || !validDial(g.Dial) || g.AuthURI == "" || g.Cookie == "" || len(a.Refused) > 0 ||
		g.Person == "" || g.Name == "" || g.Username == "" ||
		(g.Role == "") == (g.Groups == "") || g.Groups != "" && len(g.GroupSep) != 1 {
		return nil, fmt.Errorf("an app's site needs its name, its upstream and its guard: %+v", a)
	}
	copied := slices.Concat([]obj{{"handle": []obj{{"handler": "vars"}}}}, identity(g))
	// The provider's refreshed cookies, each its own header: a placeholder would join them into one with commas.
	copied = append(copied, obj{"handle": []obj{{"handler": "copy_response_headers", "include": []string{"Set-Cookie"}}}})
	guard := obj{"handler": "reverse_proxy", "upstreams": []obj{{"dial": g.Dial}},
		"rewrite":         obj{"method": "GET", "uri": g.AuthURI},
		"headers":         obj{"request": obj{"set": obj{"X-Forwarded-Method": []string{"{http.request.method}"}, "X-Forwarded-Uri": []string{"{http.request.uri}"}}}},
		"handle_response": []obj{{"match": obj{"status_code": []int{2}}, "routes": copied}}}
	// The provider's cookies, out of the Cookie header the app gets; a Set-Cookie of the app's for a household domain,
	// emptied (a browser ignores an empty one).
	cookie := regexp.QuoteMeta(g.Cookie) + `[^=;]*=[^;]*(;\s*|$)`
	app := obj{"handler": "headers", "request": obj{"replace": obj{"Cookie": []obj{{"search_regexp": cookie, "replace": ""}}}}}
	if ownDomain := ownDomainCookies(domains); ownDomain != nil {
		app["response"] = ownDomain
	}
	var own []obj
	if len(g.Paths) > 0 {
		own = []obj{{"match": []obj{{"path": g.Paths}}, "handle": []obj{{"handler": "reverse_proxy", "upstreams": []obj{{"dial": g.Dial}}}}, "terminal": true}}
	}
	sub := slices.Concat(
		[]obj{{"handle": []obj{{"handler": "headers", "request": obj{"delete": identitySpellings}}}}},
		own,
		[]obj{{"handle": []obj{guard}}, {"handle": []obj{app, {"handler": "reverse_proxy", "upstreams": []obj{{"dial": a.Dial}}}}}},
	)
	return obj{"handler": "subroute", "routes": sub}, nil
}

// ownDomainCookies is the response rule that empties an app's Set-Cookie for a household domain, or any domain above it
// (a browser takes a cookie for example.com on home.example.com's names), with or without its dots at either end (#83).
func ownDomainCookies(domains []string) obj {
	wide := parents(domains)
	if len(wide) == 0 {
		return nil
	}
	return obj{"replace": obj{"Set-Cookie": []obj{{"search_regexp": `(?i)^.*;\s*domain\s*=\s*\.?(` + strings.Join(wide, "|") + `)\.?\s*(;.*)?$`, "replace": ""}}},
		"deferred": true}
}

// standardMethods are the methods a refused route may name, and the only ones that reach an app with a route
// refused by method.
var standardMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// nativeRoute is the site of an app that signs people in itself (#20): no guard, but the client's identity headers
// deleted first all the same (an app may read one), and no cookie of the app's for a whole household domain (#83). Like
// appRoute's, it is the site's handler, which Render serves with HSTS and Secure cookies.
func nativeRoute(a App, domains []string) (obj, error) {
	if a.Name == "" || !validDial(a.Dial) || a.Guard != nil {
		return nil, fmt.Errorf("an app's site needs its name and its upstream, and a guard or its own sign-in, not both: %+v", a)
	}
	app := obj{"handler": "headers"}
	if ownDomain := ownDomainCookies(domains); ownDomain != nil {
		app["response"] = ownDomain
	}
	var sub []obj
	// Caddy matches paths case-insensitively and after unescaping; each path, and anything below it. By method, the
	// paths refused for every method first.
	byMethod := map[string][]string{}
	for _, r := range a.Refused {
		method, p, two := strings.Cut(r, " ")
		if !two {
			method, p = "", r
		} else if !slices.Contains(standardMethods, method) {
			return nil, fmt.Errorf("a refused route's method is a standard one: %q", r)
		}
		if !strings.HasPrefix(p, "/") || p == "/" || strings.ContainsAny(p, "*?# ") {
			return nil, fmt.Errorf("a refused path is one of the app's own: %q", r)
		}
		byMethod[method] = append(byMethod[method], p, strings.TrimSuffix(p, "/")+"/*")
	}
	if len(byMethod) > 1 || len(byMethod) == 1 && byMethod[""] == nil {
		// A route refused for one method only: a method spelt any other way (put, PROPFIND) never reaches the app,
		// so it can't pass beside the refusal.
		sub = append(sub, obj{"match": []obj{{"not": []obj{{"method": standardMethods}}}},
			"handle": []obj{{"handler": "static_response", "status_code": 405}}, "terminal": true})
	}
	for _, method := range slices.Sorted(maps.Keys(byMethod)) {
		m := obj{"path": byMethod[method]}
		switch method {
		case "":
		case "GET":
			m["method"] = []string{"GET", "HEAD"} // an app's GET routes answer HEAD too
		default:
			m["method"] = []string{method}
		}
		sub = append(sub, obj{"match": []obj{m}, "handle": []obj{{"handler": "static_response", "status_code": 403}}, "terminal": true})
	}
	sub = append(sub,
		obj{"handle": []obj{{"handler": "headers", "request": obj{"delete": identitySpellings}}}},
		obj{"handle": []obj{app, {"handler": "reverse_proxy", "upstreams": []obj{{"dial": a.Dial}}}}},
	)
	return obj{"handler": "subroute", "routes": sub}, nil
}

// identity are the routes of a guard's 2xx answer that keep the identity contract (Contract): the person's id, name
// (their username when the provider has no name for them) and role, set on the request the app gets; or 403, when the
// answer names no id, no name at all, or no role: not exactly one role group, or no role the provider names exactly.
func identity(g *Guard) []obj {
	ph := func(h string) string { return "{http.reverse_proxy.header." + h + "}" }
	empty := func(h string) obj { return obj{"vars": obj{ph(h): []string{""}}} }
	sep := regexp.QuoteMeta(g.GroupSep)
	in := func(r idp.Role) obj {
		if g.Role != "" { // the provider's own answer, exactly
			return obj{"vars": obj{ph(g.Role): []string{string(r)}}}
		}
		return obj{"vars_regexp": obj{ph(g.Groups): obj{"pattern": "(^|" + sep + ")" + regexp.QuoteMeta(idp.RoleGroup(r)) + "(" + sep + "|$)"}}}
	}
	set := func(h, v string) []obj {
		return []obj{{"handler": "headers", "request": obj{"set": obj{h: []string{v}}}}}
	}
	var onlyOne, roles []obj // a matcher per role: in its group, and in no other role's
	for _, r := range idp.Roles() {
		m, others := in(r), []obj{}
		for _, o := range idp.Roles() {
			if o != r {
				others = append(others, in(o))
			}
		}
		m["not"] = others
		onlyOne = append(onlyOne, m)
		roles = append(roles, obj{"match": []obj{m}, "handle": set(RoleHeader, string(r))})
	}
	refused := obj{"match": []obj{empty(g.Person), {"not": []obj{{"not": []obj{empty(g.Name)}}, {"not": []obj{empty(g.Username)}}}}, {"not": onlyOne}},
		"handle": []obj{{"handler": "static_response", "status_code": 403}}}
	return slices.Concat([]obj{refused}, roles, []obj{
		{"handle": set(PersonHeader, ph(g.Person))},
		{"match": []obj{empty(g.Name)}, "handle": set(NameHeader, ph(g.Username))},
		{"match": []obj{{"not": []obj{empty(g.Name)}}}, "handle": set(NameHeader, ph(g.Name))},
	})
}

// parents are the household's domains and every domain above them, quoted for a regular expression, in order.
func parents(domains []string) []string {
	var out []string
	for _, d := range domains {
		labels := strings.Split(strings.Trim(strings.ToLower(d), "."), ".")
		for i := range labels {
			if p := strings.Join(labels[i:], "."); p != "" {
				out = append(out, regexp.QuoteMeta(p))
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func validDial(d string) bool {
	_, err := netip.ParseAddrPort(d)
	return err == nil
}

// obj is a JSON object; encoding/json writes its keys in order, so a render is deterministic.
type obj = map[string]any

// hostPort is a name at a port, leaving out 443.
func hostPort(name string, port int) string {
	if port == 443 {
		return name
	}
	return name + ":" + strconv.Itoa(port)
}
