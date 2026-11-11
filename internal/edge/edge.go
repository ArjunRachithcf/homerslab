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
}

// Certificate is one domain's wildcard certificate, loaded from files.
type Certificate struct {
	Domain            string
	CertFile, KeyFile string
}

// Site is one name the edge serves, proxied to a Unix socket.
type Site struct {
	Name     string
	Upstream string
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

// obj is a JSON object; encoding/json writes its keys in order, so a render is deterministic.
type obj = map[string]any
