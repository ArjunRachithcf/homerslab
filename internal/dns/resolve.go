// Package dns is the DNS provider contract (spec: Adapter contracts → DNS provider; ADR 0016): the household's DNS
// provider through libdns, the records HomersLab owns there and nothing else (owned.go), and the lookups the
// certificate's DNS challenge needs.
//
// Lookups ask public resolvers, never the machine's own (#79): on the primary, the household's domain resolves
// through the mesh's own zone, which has no SOA and none of the public records. The propagation check asks the zone's
// authoritative servers for the exact record, and never follows a CNAME, because the public path's wildcard CNAME
// also answers for _acme-challenge.<domain>.
package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	mdns "github.com/miekg/dns"
)

// Public are the resolvers HomersLab asks by default: Quad9's and Cloudflare's, over IPv4 and IPv6.
var Public = []string{"9.9.9.9:53", "1.1.1.1:53", "149.112.112.112:53", "[2620:fe::fe]:53", "[2606:4700:4700::1111]:53"}

// Resolver looks names up in the public DNS.
type Resolver struct {
	// Servers are recursive resolvers, host:port; Public when empty. Never the machine's own resolver.
	Servers []string
	// Port is the port the zones' authoritative servers answer on: "53" when empty (a test's servers listen
	// elsewhere).
	Port    string
	Timeout time.Duration
	// exchange sends one question to one server; the network's when nil (a test's stand-in otherwise).
	exchange func(ctx context.Context, q *mdns.Msg, server string) (*mdns.Msg, error)
}

// ErrNoZone is a name no public zone holds.
var ErrNoZone = errors.New("no public DNS zone holds this name")

func (r Resolver) servers() []string {
	if len(r.Servers) == 0 {
		return Public
	}
	return r.Servers
}

func (r Resolver) client() *mdns.Client {
	t := r.Timeout
	if t == 0 {
		t = 5 * time.Second
	}
	return &mdns.Client{Timeout: t}
}

// ask sends one question to each server in turn until one answers with NOERROR or NXDOMAIN.
func (r Resolver) ask(ctx context.Context, servers []string, name string, qtype uint16, recurse bool) (*mdns.Msg, error) {
	q := new(mdns.Msg)
	q.SetQuestion(mdns.Fqdn(name), qtype)
	q.RecursionDesired = recurse
	q.SetEdns0(1232, false)
	var last error = errors.New("no server to ask")
	for _, s := range servers {
		resp, err := r.send(ctx, q, s)
		switch {
		case err != nil:
			last = err
		case resp.Rcode == mdns.RcodeSuccess || resp.Rcode == mdns.RcodeNameError:
			return resp, nil
		default:
			last = fmt.Errorf("%s answered %s for %s", s, mdns.RcodeToString[resp.Rcode], name)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, last
}

// send sends one question to one server, again over TCP when the answer was truncated.
func (r Resolver) send(ctx context.Context, q *mdns.Msg, server string) (*mdns.Msg, error) {
	if r.exchange != nil {
		return r.exchange(ctx, q, server)
	}
	resp, _, err := r.client().ExchangeContext(ctx, q, server)
	if err == nil && resp.Truncated {
		c := r.client()
		c.Net = "tcp"
		resp, _, err = c.ExchangeContext(ctx, q, server)
	}
	return resp, err
}

// Zone is the public zone that holds name, as libdns names it (with a trailing dot). It looks for the zone's SOA,
// one label up at a time, and ignores a CNAME on the way: _acme-challenge.<domain> may be answered by a wildcard
// CNAME that points into someone else's zone.
func (r Resolver) Zone(ctx context.Context, name string) (string, error) {
	labels := mdns.SplitDomainName(name)
	for i := range labels {
		candidate := mdns.Fqdn(strings.Join(labels[i:], "."))
		resp, err := r.ask(ctx, r.servers(), candidate, mdns.TypeSOA, true)
		if err != nil {
			return "", fmt.Errorf("looking for the zone of %s: %w", name, err)
		}
		for _, rr := range resp.Answer {
			if soa, ok := rr.(*mdns.SOA); ok && strings.EqualFold(soa.Hdr.Name, candidate) {
				return strings.ToLower(candidate), nil
			}
		}
		for _, rr := range resp.Ns {
			// A name that doesn't exist, or holds no SOA, comes back with its zone's SOA; one that led somewhere else
			// (a CNAME a resolver followed) comes back with that other zone's, which isn't ours.
			if soa, ok := rr.(*mdns.SOA); ok && mdns.IsSubDomain(soa.Hdr.Name, candidate) && !hasCNAME(resp, candidate) {
				return strings.ToLower(mdns.Fqdn(soa.Hdr.Name)), nil
			}
		}
	}
	return "", fmt.Errorf("%w: %s", ErrNoZone, name)
}

func hasCNAME(resp *mdns.Msg, name string) bool {
	return slices.ContainsFunc(resp.Answer, func(rr mdns.RR) bool {
		_, ok := rr.(*mdns.CNAME)
		return ok && strings.EqualFold(rr.Header().Name, name)
	})
}

// Authoritative are a zone's authoritative servers, by name, with their addresses, host:port.
func (r Resolver) Authoritative(ctx context.Context, zone string) (map[string][]string, error) {
	resp, err := r.ask(ctx, r.servers(), zone, mdns.TypeNS, true)
	if err != nil {
		return nil, fmt.Errorf("the name servers of %s: %w", zone, err)
	}
	port := r.Port
	if port == "" {
		port = "53"
	}
	out := map[string][]string{}
	for _, rr := range resp.Answer {
		ns, ok := rr.(*mdns.NS)
		if !ok || !strings.EqualFold(ns.Hdr.Name, zone) {
			continue
		}
		name := strings.ToLower(ns.Ns)
		for _, t := range []uint16{mdns.TypeA, mdns.TypeAAAA} {
			a, err := r.ask(ctx, r.servers(), ns.Ns, t, true)
			if err != nil {
				continue
			}
			for _, rr := range a.Answer {
				switch x := rr.(type) {
				case *mdns.A:
					out[name] = append(out[name], net.JoinHostPort(x.A.String(), port))
				case *mdns.AAAA:
					out[name] = append(out[name], net.JoinHostPort(x.AAAA.String(), port))
				}
			}
		}
		if _, ok := out[name]; !ok {
			out[name] = nil // a name server without an address: nothing of it can say it holds a record
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no name server", zone)
	}
	return out, nil
}

// HasTXT says whether the zone's authoritative servers hold a TXT record at exactly fqdn with exactly value. It asks
// each of their addresses directly, without recursion; an address that doesn't answer at all (an IPv6 address on a
// primary without IPv6, a server that is down) is skipped. Every name server needs at least one address that answers,
// and every address that answers must hold the record. A CNAME never counts.
func (r Resolver) HasTXT(ctx context.Context, zone, fqdn, value string) (bool, error) {
	servers, err := r.Authoritative(ctx, zone)
	if err != nil {
		return false, err
	}
	q := new(mdns.Msg)
	q.SetQuestion(mdns.Fqdn(fqdn), mdns.TypeTXT)
	q.RecursionDesired = false
	q.SetEdns0(1232, false)
	for _, addrs := range servers {
		answered := false
		for _, s := range addrs {
			resp, err := r.send(ctx, q, s)
			if err != nil {
				continue // no answer from this address: another of the server's may answer
			}
			answered = true
			found := resp.Rcode == mdns.RcodeSuccess && slices.ContainsFunc(resp.Answer, func(rr mdns.RR) bool {
				txt, ok := rr.(*mdns.TXT)
				return ok && strings.EqualFold(txt.Hdr.Name, mdns.Fqdn(fqdn)) && strings.Join(txt.Txt, "") == value
			})
			if !found {
				return false, nil
			}
		}
		if !answered {
			return false, nil // not one address of this server answered: ask again later
		}
	}
	return true, nil
}

// CAA is one CAA record: which certificate authority may issue for a domain.
type CAA struct {
	Name  string `json:"name"`
	Flags uint8  `json:"flags"`
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

// CAA is the CAA record set that applies to name, as a certificate authority finds it: the set at the name itself,
// or else at the closest parent that has one (RFC 8659 §3). None means any authority may issue.
func (r Resolver) CAA(ctx context.Context, name string) ([]CAA, error) {
	labels := mdns.SplitDomainName(strings.TrimPrefix(name, "*."))
	for i := range labels {
		candidate := mdns.Fqdn(strings.Join(labels[i:], "."))
		resp, err := r.ask(ctx, r.servers(), candidate, mdns.TypeCAA, true)
		if err != nil {
			if i == len(labels)-1 { // a top-level domain that refuses to say has no records for us
				return nil, nil
			}
			return nil, fmt.Errorf("the CAA records of %s: %w", candidate, err)
		}
		var out []CAA
		for _, rr := range resp.Answer {
			if c, ok := rr.(*mdns.CAA); ok {
				out = append(out, CAA{Name: strings.ToLower(c.Hdr.Name), Flags: c.Flag, Tag: strings.ToLower(c.Tag), Value: c.Value})
			}
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return nil, nil
}

// AllowsIssuer says whether a CAA set lets an authority known by one of ids issue a wildcard certificate: issuewild
// rules a wildcard where the set has any, issue otherwise; an empty set allows everyone.
func AllowsIssuer(set []CAA, ids []string) bool {
	var issue, wild []string
	for _, c := range set {
		v, _, _ := strings.Cut(c.Value, ";")
		v = strings.ToLower(strings.TrimSpace(v))
		switch c.Tag {
		case "issue":
			issue = append(issue, v)
		case "issuewild":
			wild = append(wild, v)
		}
	}
	rules := issue
	if len(wild) > 0 {
		rules = wild
	}
	if len(rules) == 0 {
		return true
	}
	for _, v := range rules {
		if slices.Contains(ids, v) {
			return true
		}
	}
	return false
}

// Issuers are the issuer domains a CAA set allows for a wildcard, for saying what it allows.
func Issuers(set []CAA) []string {
	var issue, wild []string
	for _, c := range set {
		v, _, _ := strings.Cut(c.Value, ";")
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		switch c.Tag {
		case "issue":
			issue = append(issue, v)
		case "issuewild":
			wild = append(wild, v)
		}
	}
	if len(wild) > 0 {
		return wild
	}
	return issue
}
