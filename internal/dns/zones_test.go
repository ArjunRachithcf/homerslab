package dns_test

import (
	"net"
	"strings"
	"sync"
	"testing"

	mdns "github.com/miekg/dns"
)

// zones is a DNS server for a test, on 127.0.0.1: authoritative for its zones, and, asked with recursion, a resolver
// that follows CNAMEs into its other zones, as public resolvers do.
type zones struct {
	mu    sync.Mutex
	mesh  bool // answers like a mesh's local zone: its records, and NOERROR with nothing for anything else
	rrs   []mdns.RR
	addr  string
	asked []string // every question, as "TYPE name"
}

func serve(t *testing.T, records ...string) *zones {
	t.Helper()
	z := &zones{}
	for _, r := range records {
		z.add(t, r)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	z.addr = pc.LocalAddr().String()
	srv := &mdns.Server{PacketConn: pc, Handler: z}
	go srv.ActivateAndServe()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return z
}

func (z *zones) port() string { _, p, _ := net.SplitHostPort(z.addr); return p }

func (z *zones) add(t *testing.T, record string) {
	t.Helper()
	rr, err := mdns.NewRR(record)
	if err != nil {
		t.Fatal(err)
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	z.rrs = append(z.rrs, rr)
}

func (z *zones) questions() []string {
	z.mu.Lock()
	defer z.mu.Unlock()
	return append([]string(nil), z.asked...)
}

// zoneOf is the closest zone (by its SOA) that holds name.
func (z *zones) zoneOf(name string) *mdns.SOA {
	var best *mdns.SOA
	for _, rr := range z.rrs {
		if soa, ok := rr.(*mdns.SOA); ok && mdns.IsSubDomain(soa.Hdr.Name, name) && (best == nil || len(soa.Hdr.Name) > len(best.Hdr.Name)) {
			best = soa
		}
	}
	return best
}

func (z *zones) at(name string) []mdns.RR {
	var out []mdns.RR
	for _, rr := range z.rrs {
		if strings.EqualFold(rr.Header().Name, name) {
			out = append(out, rr)
		}
	}
	return out
}

func (z *zones) ServeDNS(w mdns.ResponseWriter, q *mdns.Msg) {
	z.mu.Lock()
	defer z.mu.Unlock()
	qn, qt := mdns.Fqdn(strings.ToLower(q.Question[0].Name)), q.Question[0].Qtype
	z.asked = append(z.asked, mdns.TypeToString[qt]+" "+qn)
	m := new(mdns.Msg)
	m.SetReply(q)
	m.Authoritative = true
	z.answer(m, qn, qt, q.RecursionDesired, 0)
	_ = w.WriteMsg(m)
}

func (z *zones) answer(m *mdns.Msg, name string, qt uint16, recurse bool, depth int) {
	if z.mesh {
		_, rest, _ := strings.Cut(name, ".")
		for _, rr := range append(z.at(name), z.at("*."+rest)...) {
			if rr.Header().Rrtype == qt {
				c := mdns.Copy(rr)
				c.Header().Name = name
				m.Answer = append(m.Answer, c)
			}
		}
		return
	}
	soa := z.zoneOf(name)
	if soa == nil {
		m.Rcode = mdns.RcodeRefused
		return
	}
	rrs := z.at(name)
	if len(rrs) == 0 && !strings.EqualFold(name, soa.Hdr.Name) {
		// A wildcard at the closest encloser answers a name that doesn't exist (RFC 4592).
		_, rest, _ := strings.Cut(name, ".")
		for _, rr := range z.at("*." + rest) {
			c := mdns.Copy(rr)
			c.Header().Name = name
			rrs = append(rrs, c)
		}
	}
	if len(rrs) == 0 {
		m.Rcode = mdns.RcodeNameError
		m.Ns = append(m.Ns, soa)
		return
	}
	var found bool
	for _, rr := range rrs {
		if rr.Header().Rrtype == qt {
			m.Answer = append(m.Answer, rr)
			found = true
		}
	}
	if found || qt == mdns.TypeCNAME {
		return
	}
	for _, rr := range rrs {
		if c, ok := rr.(*mdns.CNAME); ok {
			m.Answer = append(m.Answer, c)
			if recurse && depth < 8 {
				z.answer(m, strings.ToLower(c.Target), qt, recurse, depth+1)
			}
			return
		}
	}
	m.Ns = append(m.Ns, soa) // the name exists, without this type
}
