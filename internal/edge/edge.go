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
	"net/netip"
	"regexp"
	"slices"
)

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
	// Apps are this node's apps' sites.
	Apps []App
}

// App is an app's site: its name, proxied to the app's one upstream (Dial: an address on its bridge, which only the
// edge's user and the agent's may open, ADR 0020). Guard is the forward-auth the edge asks before any request reaches
// the app: every app so far is guarded (its sign-in method is the guard); apps that sign people in themselves come with
// their own ticket (#20).
type App struct {
	Name  string
	Dial  string
	Guard *Guard
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
	// Copy are the identity headers the provider's 2xx answer names, handed to the app; nothing else is.
	Copy []string
	// Cookie is the prefix of the provider's own cookies on the app's name, which the app never sees (#83).
	Cookie string
}

// AuthentikGuard is Authentik's embedded outpost, on its server, as the guard of an app with its own proxy provider in
// forward_single mode (#83: never one for the whole domain).
func AuthentikGuard(dial string) *Guard {
	return &Guard{Dial: dial, AuthURI: "/outpost.goauthentik.io/auth/caddy", Paths: []string{"/outpost.goauthentik.io/*"},
		Copy:   []string{"X-Authentik-Username", "X-Authentik-Groups", "X-Authentik-Email", "X-Authentik-Name", "X-Authentik-Uid"},
		Cookie: "authentik_proxy_"}
}

// IdentityHeaders are the request headers that name a person to an app behind a guard, Authentik's and those other
// providers and apps read: a client could forge any of them, so the edge deletes them all from every request to a
// guarded app before anything else, and only the guard's answer sets any again.
var IdentityHeaders = []string{"X-Authentik-*", "Remote-*", "X-Remote-*", "X-Forwarded-User", "X-Forwarded-Email",
	"X-Forwarded-Groups", "X-Forwarded-Preferred-Username", "X-Forwarded-Access-Token", "X-Auth-Request-*", "X-Webauth-*",
	"X-Token-User-*", "X-User", "X-Email", "X-Username"}

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
	if len(c.Listen) == 0 || c.Port <= 0 || c.Port > 65535 {
		return nil, errors.New("the edge needs the node's mesh addresses and the household's port")
	}
	listen := make([]string, 0, len(c.Listen))
	for _, a := range c.Listen {
		if !a.IsValid() || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() {
			return nil, fmt.Errorf("the edge listens on the node's mesh addresses only, not %v", a)
		}
		listen = append(listen, netip.AddrPortFrom(a.Unmap(), uint16(c.Port)).String())
	}
	slices.Sort(listen)

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

	var routes []obj
	if c.Admin != nil {
		routes = append(routes, obj{
			"match":    []obj{{"host": []string{c.Admin.Name}}},
			"handle":   []obj{{"handler": "reverse_proxy", "upstreams": []obj{{"dial": "unix/" + c.Admin.Upstream}}}},
			"terminal": true,
		})
	}
	if c.SignIn != nil {
		if _, err := netip.ParseAddrPort(c.SignIn.Dial); err != nil || c.SignIn.Name == "" {
			return nil, errors.New("the sign-in needs its name and its upstream's address and port")
		}
		routes = append(routes, obj{
			"match": []obj{{"host": []string{c.SignIn.Name}}},
			"handle": []obj{{"handler": "subroute", "routes": []obj{
				{"match": []obj{{"path": FirstAdminPaths}}, "handle": []obj{{"handler": "static_response", "status_code": 403}}, "terminal": true},
				{"handle": []obj{{"handler": "reverse_proxy", "upstreams": []obj{{"dial": c.SignIn.Dial}}}}},
			}}},
			"terminal": true,
		})
	}
	domains := make([]string, 0, len(c.Certificates))
	for _, crt := range c.Certificates {
		domains = append(domains, crt.Domain)
	}
	for _, app := range c.Apps {
		r, err := appRoute(app, domains)
		if err != nil {
			return nil, err
		}
		routes = append(routes, r)
	}
	// Any other name: 421 Misdirected Request, never Caddy's empty 200 for a request no route matches (#81).
	routes = append(routes, obj{"handle": []obj{{"handler": "static_response", "status_code": 421}}})

	cfg := obj{
		"admin":   obj{"listen": AdminListen(c.AdminSocket), "config": obj{"persist": false}},
		"storage": obj{"module": "file_system", "root": c.Storage},
	}
	// Without a certificate the edge listens nowhere: Caddy would serve plain HTTP on a server without TLS.
	if loads != nil {
		cfg["apps"] = obj{
			"tls": obj{"certificates": obj{"load_files": loads}, "disable_ocsp_stapling": true},
			"http": obj{"servers": obj{"mesh": obj{
				"listen":                  listen,
				"protocols":               []string{"h1", "h2"},
				"automatic_https":         obj{"disable": true},
				"tls_connection_policies": policies,
				"routes":                  routes,
			}}},
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
// (#83).
func appRoute(a App, domains []string) (obj, error) {
	g := a.Guard
	if a.Name == "" || !validDial(a.Dial) || g == nil || !validDial(g.Dial) || g.AuthURI == "" || g.Cookie == "" {
		return nil, fmt.Errorf("an app's site needs its name, its upstream and its guard: %+v", a)
	}
	notEmpty := func(v string) []obj { return []obj{{"not": []obj{{"vars": obj{v: []string{""}}}}}} }
	copied := []obj{{"handle": []obj{{"handler": "vars"}}}}
	for _, h := range g.Copy {
		v := "{http.reverse_proxy.header." + h + "}"
		copied = append(copied, obj{"match": notEmpty(v), "handle": []obj{{"handler": "headers", "request": obj{"set": obj{h: []string{v}}}}}})
	}
	const refreshed = "{http.reverse_proxy.header.Set-Cookie}"
	copied = append(copied, obj{"match": notEmpty(refreshed), "handle": []obj{{"handler": "headers", "response": obj{"add": obj{"Set-Cookie": []string{refreshed}}}}}})
	guard := obj{"handler": "reverse_proxy", "upstreams": []obj{{"dial": g.Dial}},
		"rewrite":         obj{"method": "GET", "uri": g.AuthURI},
		"headers":         obj{"request": obj{"set": obj{"X-Forwarded-Method": []string{"{http.request.method}"}, "X-Forwarded-Uri": []string{"{http.request.uri}"}}}},
		"handle_response": []obj{{"match": obj{"status_code": []int{2}}, "routes": copied}}}
	// The provider's cookies, out of the Cookie header the app gets; a Set-Cookie of the app's for a household domain,
	// emptied (a browser ignores an empty one).
	cookie := regexp.QuoteMeta(g.Cookie) + `[^=;]*=[^;]*(;\s*|$)`
	var ownDomain []obj
	for _, d := range domains {
		ownDomain = append(ownDomain, obj{"search_regexp": `(?i)^.*;\s*domain\s*=\s*\.?` + regexp.QuoteMeta(d) + `\s*(;.*)?$`, "replace": ""})
	}
	app := obj{"handler": "headers", "request": obj{"replace": obj{"Cookie": []obj{{"search_regexp": cookie, "replace": ""}}}}}
	if ownDomain != nil {
		app["response"] = obj{"replace": obj{"Set-Cookie": ownDomain}, "deferred": true}
	}
	var own []obj
	if len(g.Paths) > 0 {
		own = []obj{{"match": []obj{{"path": g.Paths}}, "handle": []obj{{"handler": "reverse_proxy", "upstreams": []obj{{"dial": g.Dial}}}}, "terminal": true}}
	}
	sub := slices.Concat(
		[]obj{{"handle": []obj{{"handler": "headers", "request": obj{"delete": IdentityHeaders}}}}},
		own,
		[]obj{{"handle": []obj{guard}}, {"handle": []obj{app, {"handler": "reverse_proxy", "upstreams": []obj{{"dial": a.Dial}}}}}},
	)
	return obj{"match": []obj{{"host": []string{a.Name}}}, "handle": []obj{{"handler": "subroute", "routes": sub}}, "terminal": true}, nil
}

func validDial(d string) bool {
	_, err := netip.ParseAddrPort(d)
	return err == nil
}

// obj is a JSON object; encoding/json writes its keys in order, so a render is deterministic.
type obj = map[string]any
