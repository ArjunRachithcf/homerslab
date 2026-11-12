package manager_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/libdns/libdns"

	"github.com/ArjunRachithcf/homerslab/internal/dns"
	"github.com/ArjunRachithcf/homerslab/internal/manager"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The value the drift notes' records carry: the drift report says a record's name and type, never its value.
const driftValue = "value-that-never-reaches-the-report"

func (p *primary) drift(t *testing.T) []struct{ Source, What string } {
	t.Helper()
	var d []struct{ Source, What string }
	p.control("GET", "/api/v1/drift", nil).json(t, &d)
	return d
}

// The DNS package's drift notes (a record deleted or replaced at the provider by hand, a listing that looked empty)
// reach the drift report, beside the mesh's, each with its record's type and name, never its value (#175).
func TestDNSDriftNotesReachTheDriftReport(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, fd := atDomain(t)
	rec := func(name string) dns.Record {
		return dns.Record{Name: name, Type: "TXT", Value: driftValue, TTL: time.Minute}
	}
	fd.drift = []dns.Drift{
		{Zone: "example.test.", Record: rec("_acme-challenge"), What: dns.DriftDeletedByHand},
		{Zone: "example.test.", Record: rec("_homerslab"), What: dns.DriftReplacedByHand},
		{Zone: "example.test.", Record: rec("_acme-challenge"), What: dns.DriftListingLookedEmpty},
	}
	throughDomain(t, p, b)

	d := p.drift(t)
	if len(d) != 3 {
		t.Fatalf("the drift report: %+v, want the three notes", d)
	}
	// Newest first.
	for i, want := range []struct{ name, how string }{
		{"_acme-challenge", "empty"}, {"_homerslab", "replaced"}, {"_acme-challenge", "deleted"},
	} {
		w := d[i].What
		if d[i].Source != "dns" || !strings.Contains(w, "TXT "+want.name) || !strings.Contains(w, "example.test") ||
			!strings.Contains(w, want.how) {
			t.Errorf("note %d: %+v, want source dns, TXT %s in example.test, %q", i, d[i], want.name, want.how)
		}
		if strings.Contains(w, driftValue) {
			t.Errorf("note %d carries the record's value: %q", i, w)
		}
	}
	assertNoSecretAt(t, p, dnsToken)
}

// An empty listing at the provider proves nothing, so no record was written: it has its own code, dns-listing-empty,
// never issuance-failed, and a renewal it stops is tried again at the scheduler's next run, without the warning to
// replace the token (#175). Any other issuance failure keeps its code.
func TestAnEmptyListingHasItsOwnCodeAndIsTriedAgainOnTheSchedule(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, fd := atDomain(t)
	empty := fmt.Errorf("the certificate for *.example.test: presenting for challenge: %w: example.test.", dns.ErrListingEmpty)

	b.mustAnswer("domain", infomaniak)
	fd.fail = empty
	if r := p.secret("dns", dnsToken); r.Code != http.StatusServiceUnavailable || r.errorCode(t) != "dns-listing-empty" {
		t.Errorf("an empty listing during setup: %d %s, want 503 dns-listing-empty", r.Code, r.Body)
	}
	fd.fail = errors.New("the ACME account at Let's Encrypt: rate limited")
	if r := p.secret("dns", dnsToken); r.Code != http.StatusBadGateway || r.errorCode(t) != "issuance-failed" {
		t.Errorf("another failure: %d %s, want 502 issuance-failed", r.Code, r.Body)
	}
	fd.fail = nil
	if r := p.secret("dns", dnsToken); r.Code != http.StatusOK {
		t.Fatalf("the DNS token once the listing is back: %d %s", r.Code, r.Body)
	}

	// A renewal the empty listing stops: no warning, counted, and tried again at the next run.
	ctx := context.Background()
	p.clock.Add(60 * 24 * time.Hour)
	fd.fail = empty
	p.m.RunDue(ctx)
	if ws := p.warnings(t); len(ws) != 0 {
		t.Errorf("an empty listing raised a warning: %+v", ws)
	}
	if r := p.control("GET", "/api/v1/dns", nil); !strings.Contains(string(r.Body), `"failures":1`) {
		t.Errorf("the failed renewal isn't counted: %s", r.Body)
	}
	fd.fail = nil
	p.clock.Add(time.Hour)
	p.m.RunDue(ctx)
	if fd.count() != 2 {
		t.Fatalf("the next run didn't renew: issued %d", fd.count())
	}

	// From the console too: its own code, and no warning.
	p.clock.Add(60 * 24 * time.Hour)
	fd.fail = empty
	if r := p.control("PUT", "/api/v1/dns/token", map[string]string{"value": dnsToken}); r.Code != http.StatusServiceUnavailable ||
		r.errorCode(t) != "dns-listing-empty" {
		t.Errorf("an empty listing from the console: %d %s, want 503 dns-listing-empty", r.Code, r.Body)
	}
	if ws := p.warnings(t); len(ws) != 0 {
		t.Errorf("an empty listing from the console raised a warning: %+v", ws)
	}

	// A listing that stays empty for a day raises a warning of its own, once.
	for range 23 {
		p.clock.Add(time.Hour)
		p.m.RunDue(ctx)
	}
	if ws := p.warnings(t); len(ws) != 0 {
		t.Fatalf("a warning before a day of empty listings: %+v", ws)
	}
	p.clock.Add(time.Hour)
	p.m.RunDue(ctx)
	p.clock.Add(time.Hour)
	p.m.RunDue(ctx)
	ws := p.warnings(t)
	if len(ws) != 1 || ws[0].Source != "certificate:example.test" || !strings.Contains(ws[0].What, "empty") ||
		!strings.Contains(ws[0].What, "*.example.test") {
		t.Fatalf("after a day of empty listings: %+v", ws)
	}
	fd.fail = nil
	p.clock.Add(time.Hour)
	p.m.RunDue(ctx)
	if ws := p.warnings(t); len(ws) != 1 || ws[0].Resolved.IsZero() {
		t.Errorf("the warning after the renewal: %+v", ws)
	}
}

// A renewal that failed first on an empty listing, then on a token the provider refuses, still raises the warning to
// replace the token: the empty listing never uses up the first failure's warning (#175).
func TestAnEmptyListingFirstStillLeavesTheTokensWarning(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, fd := atDomain(t)
	throughDomain(t, p, b)
	ctx := context.Background()

	p.clock.Add(60 * 24 * time.Hour)
	fd.fail = fmt.Errorf("presenting for challenge: %w", dns.ErrListingEmpty)
	p.m.RunDue(ctx)
	fd.fail = manager.ErrDNSRefused
	p.clock.Add(time.Hour)
	p.m.RunDue(ctx)
	ws := p.warnings(t)
	if len(ws) != 1 || !strings.Contains(ws[0].What, "sudo homerslab dns token") {
		t.Fatalf("after an empty listing, then a refused token: %+v", ws)
	}
	p.clock.Add(time.Hour)
	p.m.RunDue(ctx)
	if ws := p.warnings(t); len(ws) != 1 {
		t.Errorf("a further failure raised another warning: %+v", ws)
	}
}

// emptyZone is a DNS provider whose listing of the zone comes back empty.
type emptyZone struct{ appended int }

func (z *emptyZone) GetRecords(context.Context, string) ([]libdns.Record, error) { return nil, nil }

func (z *emptyZone) AppendRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	z.appended += len(recs)
	return recs, nil
}

func (z *emptyZone) DeleteRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	return nil, nil
}

// noBook is a book that lists no records of HomersLab's.
type noBook struct{}

func (noBook) Records(context.Context, string) ([]dns.Record, error) { return nil, nil }
func (noBook) Keep(context.Context, string, dns.Record) error        { return nil }
func (noBook) Forget(context.Context, string, dns.Record) error      { return nil }

// The zone the manager's real domains give the certificate authority says its drift notes to the issuance (#175).
func TestTheRealZoneSaysItsDriftToTheIssuance(t *testing.T) {
	testenv.Isolate(t)
	var notes []dns.Drift
	i := manager.Issuance{Zone: "example.test.", Domain: "example.test", Drift: func(d dns.Drift) { notes = append(notes, d) }}
	z := &emptyZone{}
	o := manager.IssuanceZone(i, z, noBook{})
	err := o.Create(context.Background(), dns.Record{Name: "_acme-challenge", Type: "TXT", Value: "token", TTL: time.Minute})
	if !errors.Is(err, dns.ErrListingEmpty) || z.appended != 0 {
		t.Fatalf("a create on an empty listing: %v, appended %d", err, z.appended)
	}
	if len(notes) != 1 || notes[0].What != dns.DriftListingLookedEmpty || notes[0].Record.Name != "_acme-challenge" {
		t.Errorf("the issuance's drift notes: %+v", notes)
	}
}
