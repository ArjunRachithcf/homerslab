package dns_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/dns"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The household's public zone with the public path's wildcard CNAME to NetBird's cluster, which lives in a zone of
// its own (classic's zone, 2026-10-21).
var publicZone = []string{
	"example.test. 60 IN SOA ns.example.test. admin.example.test. 1 7200 3600 1209600 60",
	"example.test. 60 IN NS ns.example.test.",
	"ns.example.test. 60 IN A 127.0.0.1",
	"*.example.test. 60 IN CNAME proxy.cluster.test.",
	"cluster.test. 60 IN SOA ns.cluster.test. admin.cluster.test. 1 7200 3600 1209600 60",
	"proxy.cluster.test. 60 IN A 192.0.2.80",
}

func resolverFor(z *zones) dns.Resolver {
	return dns.Resolver{Servers: []string{z.addr}, Port: z.port()}
}

// The zone is found by its SOA, one label up at a time, past the wildcard CNAME that answers for
// _acme-challenge.<domain> and the other zone a resolver follows it into (#79).
func TestTheZoneIsFoundPastThePublicWildcard(t *testing.T) {
	testenv.Isolate(t)
	z := serve(t, publicZone...)
	r := resolverFor(z)
	ctx := context.Background()
	for _, name := range []string{"_acme-challenge.example.test", "example.test", "photos.example.test", "a.b.example.test"} {
		got, err := r.Zone(ctx, name)
		if err != nil || got != "example.test." {
			t.Errorf("the zone of %s: %q %v, want example.test.", name, got, err)
		}
	}
}

// A resolver that answers like the mesh's own zone on the primary (A records, no SOA) gives no zone: the reason the
// DNS challenge never asks the machine's own resolver (#79).
func TestAZoneWithoutSOAIsNoZone(t *testing.T) {
	testenv.Isolate(t)
	z := serve(t, "*.example.test. 60 IN A 100.88.7.186")
	z.mesh = true
	r := resolverFor(z)
	if _, err := r.Zone(context.Background(), "_acme-challenge.example.test"); !errors.Is(err, dns.ErrNoZone) {
		t.Fatalf("the zone through a mesh's own resolver: %v, want ErrNoZone", err)
	}
	if q := z.questions(); !slices.Contains(q, "SOA example.test.") {
		t.Errorf("the questions: %v", q)
	}
}

// The propagation check asks the zone's own servers for the exact TXT record, and never takes the wildcard's CNAME for
// it (#79).
func TestThePropagationCheckWantsTheExactRecordFromTheZonesServers(t *testing.T) {
	testenv.Isolate(t)
	z := serve(t, publicZone...)
	r := resolverFor(z)
	ctx := context.Background()
	const fqdn, value = "_acme-challenge.example.test.", "K3p5R6T9W0X2YDA6C8E0G2J4L6N8Q0S2U4W7HQD00"
	if ok, err := r.HasTXT(ctx, "example.test.", fqdn, value); err != nil || ok {
		t.Fatalf("before the record exists, with only the wildcard's CNAME there: %v %v, want not there", ok, err)
	}
	z.add(t, fqdn+` 60 IN TXT "something-else"`)
	if ok, _ := r.HasTXT(ctx, "example.test.", fqdn, value); ok {
		t.Error("another TXT value counted as ours")
	}
	z.add(t, fqdn+` 60 IN TXT "`+value+`"`)
	if ok, err := r.HasTXT(ctx, "example.test.", fqdn, value); err != nil || !ok {
		t.Fatalf("with the record there: %v %v", ok, err)
	}
	var direct bool
	for _, q := range z.questions() {
		if q == "TXT "+fqdn {
			direct = true
		}
	}
	if !direct {
		t.Errorf("the TXT record was never asked for by its exact name: %v", z.questions())
	}
}

// The CAA records that apply are the closest ones up the tree; issuewild rules a wildcard certificate (#80).
func TestCAARecordsSayWhichAuthorityMayIssue(t *testing.T) {
	testenv.Isolate(t)
	z := serve(t, publicZone...)
	r := resolverFor(z)
	ctx := context.Background()
	le := []string{"letsencrypt.org"}

	set, err := r.CAA(ctx, "*.example.test")
	if err != nil || len(set) != 0 || !dns.AllowsIssuer(set, le) {
		t.Fatalf("no CAA record: %v %v, want any authority allowed", set, err)
	}
	z.add(t, `example.test. 60 IN CAA 0 issue "sectigo.com"`)
	set, _ = r.CAA(ctx, "*.example.test")
	if dns.AllowsIssuer(set, le) {
		t.Errorf("CAA %v allows Let's Encrypt", set)
	}
	if got := dns.Issuers(set); !slices.Equal(got, []string{"sectigo.com"}) {
		t.Errorf("the allowed issuers: %v", got)
	}
	z.add(t, `example.test. 60 IN CAA 0 issuewild "letsencrypt.org; validationmethods=dns-01"`)
	set, _ = r.CAA(ctx, "*.example.test")
	if !dns.AllowsIssuer(set, le) {
		t.Errorf("issuewild for Let's Encrypt, beside issue for another: %v, want allowed for a wildcard", set)
	}
	if dns.AllowsIssuer([]dns.CAA{{Tag: "issue", Value: ";"}}, le) {
		t.Error(`issue ";" allows an authority`)
	}
}
