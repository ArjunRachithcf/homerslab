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
	"strconv"
)

// HSTS is the Strict-Transport-Security every name the edge serves answers with (#64): a year, without
// includeSubDomains or preload, because a household's domain may also carry sites that aren't HomersLab's. It replaces
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
}

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
	if c.SignIn != nil {
		if _, err := netip.ParseAddrPort(c.SignIn.Dial); err != nil || c.SignIn.Name == "" {
			return nil, errors.New("the sign-in needs its name and its upstream's address and port")
		}
		sites = append(sites, served{c.SignIn.Name, obj{"handler": "subroute", "routes": []obj{
			{"match": []obj{{"path": FirstAdminPaths}}, "handle": []obj{{"handler": "static_response", "status_code": 403}}, "terminal": true},
			{"handle": []obj{{"handler": "reverse_proxy", "upstreams": []obj{{"dial": c.SignIn.Dial}}}}},
		}}})
	}
	// Each name the edge serves: HSTS and Secure cookies on whatever it answers, set as the answer is written so they
	// replace the upstream's, then what answers it. On port 80, a redirect to HTTPS on the same name, and nothing else.
	var cookies []obj
	for _, r := range secureCookie {
		cookies = append(cookies, obj{"search_regexp": r.search, "replace": r.replace})
	}
	var routes, redirects []obj
	for _, site := range sites {
		routes = append(routes, obj{
			"match": []obj{{"host": []string{site.name}}},
			"handle": []obj{
				{"handler": "headers", "response": obj{
					"set":      obj{"Strict-Transport-Security": []string{HSTS}},
					"replace":  obj{"Set-Cookie": cookies},
					"deferred": true,
				}},
				site.handle,
			},
			"terminal": true,
		})
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

	cfg := obj{
		"admin":   obj{"listen": AdminListen(c.AdminSocket), "config": obj{"persist": false}},
		"storage": obj{"module": "file_system", "root": c.Storage},
	}
	// Without a certificate the edge listens nowhere: Caddy would serve plain HTTP on a server without TLS.
	if loads != nil {
		cfg["apps"] = obj{
			"tls": obj{"certificates": obj{"load_files": loads}, "disable_ocsp_stapling": true},
			"http": obj{"servers": obj{
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
			}},
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

// obj is a JSON object; encoding/json writes its keys in order, so a render is deterministic.
type obj = map[string]any

// hostPort is a name at a port, leaving out 443.
func hostPort(name string, port int) string {
	if port == 443 {
		return name
	}
	return name + ":" + strconv.Itoa(port)
}
