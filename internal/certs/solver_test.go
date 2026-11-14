package certs_test

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libdns/libdns"
	"github.com/mholt/acmez/v3/acme"
	mdns "github.com/miekg/dns"

	"github.com/ArjunRachithcf/homerslab/internal/certs"
	"github.com/ArjunRachithcf/homerslab/internal/dns"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// zone is a DNS provider's zone in memory, through libdns, that keeps every name as it is written. Its listing holds
// the apex's SOA and NS too, as every zone's does: a listing that is empty proves nothing, and is refused (#171).
type zone struct {
	mu      sync.Mutex
	records []libdns.RR
}

func (z *zone) GetRecords(context.Context, string) ([]libdns.Record, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	out := []libdns.Record{
		libdns.RR{Name: "@", Type: "SOA", Data: "ns.example.test. admin.example.test. 1 7200 3600 1209600 60"},
		libdns.RR{Name: "@", Type: "NS", Data: "ns.example.test."},
	}
	for _, r := range z.records {
		out = append(out, r)
	}
	return out, nil
}

func (z *zone) AppendRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	for _, r := range recs {
		z.records = append(z.records, r.RR())
	}
	return recs, nil
}

func (z *zone) DeleteRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	var deleted []libdns.Record
	for _, r := range recs {
		rr := r.RR()
		z.records = slices.DeleteFunc(z.records, func(x libdns.RR) bool {
			hit := x.Name == rr.Name && x.Type == rr.Type && x.Data == rr.Data
			if hit {
				deleted = append(deleted, x)
			}
			return hit
		})
	}
	return deleted, nil
}

// at is the TXT values the zone holds at a name, as written.
func (z *zone) at(name string) []string {
	z.mu.Lock()
	defer z.mu.Unlock()
	var out []string
	for _, r := range z.records {
		if r.Name == name && r.Type == "TXT" {
			out = append(out, r.Data)
		}
	}
	return out
}

func (z *zone) names() []string {
	z.mu.Lock()
	defer z.mu.Unlock()
	var out []string
	for _, r := range z.records {
		out = append(out, r.Name)
	}
	return out
}

// challenge is the authority's DNS challenge for a domain's wildcard, as an ACME authorization names it.
func challenge(domain string) acme.Challenge {
	return acme.Challenge{Type: acme.ChallengeTypeDNS01, Identifier: acme.Identifier{Type: "dns", Value: domain},
		KeyAuthorization: "token.thumbprint"}
}

// The challenge's record is named from the zone and the domain in their one form, as the Book names records (#165):
// a zone in another ASCII case, with or without its trailing dot, or in Unicode, still puts it at _acme-challenge
// under the domain, relative to the zone; the clean-up deletes that very record.
func TestTheChallengesNameIsReadFromTheZonesOneForm(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct{ zone, domain, identifier, want string }{
		{"example.test.", "example.test", "example.test", "_acme-challenge"},
		{"example.test", "example.test", "example.test", "_acme-challenge"},
		{"Example.TEST.", "example.test", "example.test", "_acme-challenge"},
		{"EXAMPLE.test", "Example.Test.", "example.test", "_acme-challenge"},
		{"Example.Test.", "home.example.test", "home.example.test", "_acme-challenge.home"},
		{"example.test", "HOME.Example.test", "home.example.test", "_acme-challenge.home"},
		{"bücher.test.", "bücher.test", "xn--bcher-kva.test", "_acme-challenge"},
		{"XN--BCHER-KVA.test.", "bücher.test", "xn--bcher-kva.test", "_acme-challenge"},
		{"bücher.test", "home.bücher.test", "home.xn--bcher-kva.test", "_acme-challenge.home"},
	} {
		z := &zone{}
		s := certs.ChallengeSolver(certs.Authority{}, &dns.Owned{Provider: z, Zone: c.zone, Domain: c.domain, Book: &book{}})
		ch := challenge(c.identifier)
		if err := s.Present(context.Background(), ch); err != nil {
			t.Errorf("zone %q, domain %q: %v", c.zone, c.domain, err)
			continue
		}
		if got := z.at(c.want); !slices.Equal(got, []string{ch.DNS01KeyAuthorization()}) {
			t.Errorf("zone %q, domain %q: %q holds %q; the zone's names: %q", c.zone, c.domain, c.want, got, z.names())
		}
		if err := s.CleanUp(context.Background(), ch); err != nil {
			t.Errorf("zone %q, domain %q: the clean-up: %v", c.zone, c.domain, err)
		}
		if got := z.at(c.want); len(got) != 0 {
			t.Errorf("zone %q, domain %q: after the clean-up, %q still holds %q", c.zone, c.domain, c.want, got)
		}
	}
}

// A challenge for a name that isn't the domain's own (outside the zone, beside the domain in it, or under the domain)
// is refused and nothing is written: never a name the zone doesn't strip, which a provider would take as a name of its own (#165).
func TestAChallengeOutsideTheDomainIsRefused(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct{ zone, domain, identifier string }{
		{"example.test.", "example.test", "other.test"},
		{"example.test.", "example.test", "example.test.other"},
		{"example.test.", "example.test", "badexample.test"},
		{"Example.Test.", "home.example.test", "example.test"},
		{"example.test.", "home.example.test", "photos.example.test"},
		{"example.test.", "example.test", "photos.example.test"},
		{"example.test.", "example.test", "exa mple.test"},
	} {
		z := &zone{}
		s := certs.ChallengeSolver(certs.Authority{}, &dns.Owned{Provider: z, Zone: c.zone, Domain: c.domain, Book: &book{}})
		err := s.Present(context.Background(), challenge(c.identifier))
		if !errors.Is(err, dns.ErrBadName) {
			t.Errorf("zone %q, domain %q, a challenge for %q: %v, want ErrBadName", c.zone, c.domain, c.identifier, err)
		}
		if n := z.names(); len(n) != 0 {
			t.Errorf("zone %q, domain %q, a challenge for %q: the zone holds %q", c.zone, c.domain, c.identifier, n)
		}
	}
}

// authoritative is a zone's one name server for a test, on 127.0.0.1: it answers its records, matched in any case.
func authoritative(t *testing.T, records ...string) dns.Resolver {
	t.Helper()
	var rrs []mdns.RR
	for _, r := range records {
		rr, err := mdns.NewRR(r)
		if err != nil {
			t.Fatal(err)
		}
		rrs = append(rrs, rr)
	}
	h := mdns.HandlerFunc(func(w mdns.ResponseWriter, q *mdns.Msg) {
		m := new(mdns.Msg)
		m.SetReply(q)
		m.Authoritative = true
		for _, rr := range rrs {
			if strings.EqualFold(rr.Header().Name, q.Question[0].Name) && rr.Header().Rrtype == q.Question[0].Qtype {
				m.Answer = append(m.Answer, rr)
			}
		}
		_ = w.WriteMsg(m)
	})
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &mdns.Server{PacketConn: pc, Handler: h}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	_, port, _ := net.SplitHostPort(pc.LocalAddr().String())
	return dns.Resolver{Servers: []string{pc.LocalAddr().String()}, Port: port, Timeout: time.Second}
}

// The challenge's propagation is checked at the name servers of the zone in its one form too (#165): a zone without
// its trailing dot, in another case or in Unicode still finds its servers, which hold the record.
func TestTheChallengeIsAwaitedAtTheZonesOneForm(t *testing.T) {
	testenv.Isolate(t)
	ch := challenge("xn--bcher-kva.test")
	bücher := authoritative(t, "xn--bcher-kva.test. 60 IN NS ns.xn--bcher-kva.test.", "ns.xn--bcher-kva.test. 60 IN A 127.0.0.1",
		`_acme-challenge.xn--bcher-kva.test. 60 IN TXT "`+ch.DNS01KeyAuthorization()+`"`)
	for _, c := range []struct{ zone, domain string }{
		{"xn--bcher-kva.test.", "xn--bcher-kva.test"},
		{"xn--bcher-kva.test", "xn--bcher-kva.test"},
		{"XN--BCHER-KVA.Test", "bücher.test."},
		{"bücher.test", "bücher.test"},
		{"bücher.test.", "XN--bcher-kva.TEST"},
	} {
		a := certs.Authority{DNS: bücher, Propagation: 3 * time.Second, Poll: 50 * time.Millisecond}
		s := certs.ChallengeSolver(a, &dns.Owned{Provider: &zone{}, Zone: c.zone, Domain: c.domain, Book: &book{}})
		if err := s.Wait(context.Background(), ch); err != nil {
			t.Errorf("zone %q, domain %q: %v", c.zone, c.domain, err)
		}
	}
	a := certs.Authority{DNS: bücher, Propagation: 3 * time.Second, Poll: 50 * time.Millisecond}
	s := certs.ChallengeSolver(a, &dns.Owned{Provider: &zone{}, Zone: "example.test.", Domain: "example.test", Book: &book{}})
	if err := s.Wait(context.Background(), ch); !errors.Is(err, dns.ErrBadName) {
		t.Errorf("a challenge for another zone's name: %v, want ErrBadName", err)
	}
}

// emptyZone is a provider whose listing comes back empty, and that records every write.
type emptyZone struct {
	mu     sync.Mutex
	writes []string
}

func (*emptyZone) GetRecords(context.Context, string) ([]libdns.Record, error) { return nil, nil }

func (z *emptyZone) AppendRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.writes = append(z.writes, "add")
	return recs, nil
}

func (z *emptyZone) DeleteRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.writes = append(z.writes, "delete")
	return recs, nil
}

// A challenge presented while the provider's listing is empty (a moment's answer, which proves nothing) writes
// nothing, and fails with an error that may be tried again later: never as a free name (#171, #176).
func TestPresentThroughAnEmptyListingWritesNothingAndMayBeTriedAgain(t *testing.T) {
	testenv.Isolate(t)
	z, b := &emptyZone{}, &book{}
	s := certs.ChallengeSolver(certs.Authority{}, &dns.Owned{Provider: z, Zone: "example.test.", Domain: "example.test", Book: b})
	err := s.Present(context.Background(), challenge("example.test"))
	if !errors.Is(err, dns.ErrListingEmpty) {
		t.Errorf("present: %v, want ErrListingEmpty", err)
	}
	if len(z.writes) != 0 {
		t.Errorf("written through an empty listing: %v", z.writes)
	}
	if recs, _ := b.Records(context.Background(), "example.test."); len(recs) != 0 {
		t.Errorf("the book: %+v", recs)
	}
}
