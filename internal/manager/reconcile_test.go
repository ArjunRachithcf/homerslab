package manager_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/libdns/libdns"

	"github.com/ArjunRachithcf/homerslab/internal/dns"
	"github.com/ArjunRachithcf/homerslab/internal/manager"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The scheduler puts the zone's inventory record right at every run, beside the certificate's renewal, so an inventory
// left stale by a write whose inventory update failed is rewritten without another write of a record (#176, #179):
// with the zone, the domain, the DNS settings and the sealed token, whether the certificate is kept or renewed. Before
// the domain's certificate there is nothing to put right.
func TestTheSchedulerPutsTheZonesInventoryRight(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, fd := atDomain(t)
	ctx := context.Background()
	p.m.RunDue(ctx)
	if got := fd.reconciles(); len(got) != 0 {
		t.Errorf("reconciled before the domain's certificate: %+v", got)
	}
	throughDomain(t, p, b)

	p.clock.Add(time.Hour)
	p.m.RunDue(ctx) // the certificate kept
	p.clock.Add(60 * 24 * time.Hour)
	p.m.RunDue(ctx) // renewed
	if fd.count() != 2 {
		t.Fatalf("issued %d, want the first and a renewal", fd.count())
	}
	got := fd.reconciles()
	if len(got) != 2 {
		t.Fatalf("reconciled %d times in two runs", len(got))
	}
	for _, i := range got {
		if i.Zone != "example.test." || i.Domain != "example.test" || i.Token.Reveal() != dnsToken || i.Settings.Provider != "infomaniak" ||
			i.Drift == nil {
			t.Errorf("reconciled %+v, want the zone example.test. with the sealed token and the drift report", i)
		}
	}
}

// A reconcile that meets an empty listing writes nothing, and is tried again at the scheduler's next run: no warning for
// the admins, one note in the drift report however long it lasts, and the next good listing finishes it; as for the
// renewal (#175, #179). Another failure is logged, and tried again too, without a warning.
func TestAReconcileOnAnEmptyListingIsTriedAgainQuietly(t *testing.T) {
	testenv.Isolate(t)
	var logs lockedBuffer
	p, b, _, fd := atDomain(t, withLogs(&logs))
	throughDomain(t, p, b)
	ctx := context.Background()
	inventory := dns.Record{Name: dns.Inventory, Type: "TXT", Value: driftValue}
	fd.reconcileFail = fmt.Errorf("the inventory record: %w: example.test.", dns.ErrListingEmpty)
	fd.reconcileDrift = []dns.Drift{{Zone: "example.test.", Record: inventory, What: dns.DriftListingLookedEmpty}}
	before := len(fd.reconciles())
	for range 30 {
		p.clock.Add(time.Hour)
		p.m.RunDue(ctx)
	}
	if n := len(fd.reconciles()) - before; n != 30 {
		t.Errorf("reconciled %d times in 30 runs", n)
	}
	if ws := p.warnings(t); len(ws) != 0 {
		t.Errorf("an empty listing raised a warning: %+v", ws)
	}
	d := p.drift(t)
	if len(d) != 1 || d[0].Source != "dns" || !strings.Contains(d[0].What, "TXT _homerslab") || !strings.Contains(d[0].What, "empty") {
		t.Errorf("30 runs on an empty listing: the drift report %+v, want one note", d)
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("an empty listing logged an error:\n%s", logs.String())
	}

	fd.reconcileFail, fd.reconcileDrift = errors.New("the provider is down"), nil
	p.clock.Add(time.Hour)
	p.m.RunDue(ctx)
	if !strings.Contains(logs.String(), "the provider is down") {
		t.Errorf("another failure isn't logged:\n%s", logs.String())
	}
	if ws := p.warnings(t); len(ws) != 0 {
		t.Errorf("a failed reconcile raised a warning: %+v", ws)
	}
	fd.reconcileFail = nil
	p.clock.Add(time.Hour)
	p.m.RunDue(ctx)
	if n := len(fd.reconciles()) - before; n != 32 {
		t.Errorf("reconciled %d times in 32 runs", n)
	}
	assertNoSecretAt(t, p, dnsToken)
	if strings.Contains(logs.String(), dnsToken) {
		t.Error("the DNS token reached the logs")
	}
}

// While a due renewal stops on an empty listing, the zone's inventory isn't reconciled in the same run: its listing has
// just come back empty, and the two notes, the challenge's and the inventory's, would follow each other in the drift
// report at every run instead of collapsing into one (#175, #179). Once the listing is back, both run.
func TestAnEmptyListingAtTheRenewalIsOneDriftNoteWithTheReconcile(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, fd := atDomain(t)
	throughDomain(t, p, b)
	ctx := context.Background()
	note := func(name string) dns.Drift {
		return dns.Drift{Zone: "example.test.", Record: dns.Record{Name: name, Type: "TXT", Value: driftValue}, What: dns.DriftListingLookedEmpty}
	}
	empty := fmt.Errorf("presenting for challenge: %w", dns.ErrListingEmpty)
	p.clock.Add(60 * 24 * time.Hour)
	fd.fail, fd.drift = empty, []dns.Drift{note("_acme-challenge")}
	fd.reconcileFail, fd.reconcileDrift = fmt.Errorf("the inventory record: %w", dns.ErrListingEmpty), []dns.Drift{note(dns.Inventory)}
	before := len(fd.reconciles())
	for range 5 {
		p.clock.Add(time.Hour)
		p.m.RunDue(ctx)
	}
	if d := p.drift(t); len(d) != 1 {
		t.Errorf("five runs on an empty listing: %d notes, want one: %+v", len(d), d)
	}
	fd.fail, fd.drift, fd.reconcileFail, fd.reconcileDrift = nil, nil, nil, nil
	p.clock.Add(time.Hour)
	p.m.RunDue(ctx)
	if n := len(fd.reconciles()) - before; n != 1 || fd.count() != 2 {
		t.Errorf("once the listing is back: reconciled %d times, issued %d", n, fd.count())
	}
}

// staleBook is a book whose inventory record still lists a record it no longer holds, as a delete whose inventory
// update failed leaves it (#176).
type staleBook struct{ recs []dns.Record }

func (b *staleBook) Records(context.Context, string) ([]dns.Record, error) {
	return slices.Clone(b.recs), nil
}
func (b *staleBook) Keep(_ context.Context, _ string, r dns.Record) error {
	b.recs = append(b.recs, r)
	return nil
}
func (b *staleBook) Forget(_ context.Context, _ string, r dns.Record) error {
	b.recs = slices.DeleteFunc(b.recs, func(k dns.Record) bool { return k == r })
	return nil
}

// zone is a DNS provider's zone in memory: its listing, and the writes it was sent.
type zone struct {
	recs   []libdns.RR
	writes []string
	empty  bool
}

func (z *zone) GetRecords(context.Context, string) ([]libdns.Record, error) {
	if z.empty {
		return nil, nil
	}
	out := make([]libdns.Record, len(z.recs))
	for i, r := range z.recs {
		out[i] = r
	}
	return out, nil
}

func (z *zone) AppendRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	for _, r := range recs {
		z.recs = append(z.recs, r.RR())
		z.writes = append(z.writes, "add "+r.RR().Name)
	}
	return recs, nil
}

func (z *zone) DeleteRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	var deleted []libdns.Record
	for _, r := range recs {
		rr := r.RR()
		z.recs = slices.DeleteFunc(z.recs, func(x libdns.RR) bool {
			hit := x.Name == rr.Name && x.Type == rr.Type && x.Data == rr.Data
			if hit {
				deleted = append(deleted, x)
			}
			return hit
		})
		z.writes = append(z.writes, "delete "+rr.Name)
	}
	return deleted, nil
}

// The real domains' Reconcile rewrites a stale inventory record from the book and writes nothing else; on an empty
// listing it writes nothing, fails as an empty listing, and says so to the issuance's Drift (#179).
func TestTheRealDomainsReconcileOnlyTheInventory(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	media := dns.Record{Name: "media", Type: "CNAME", Value: "proxy.cluster.test.", TTL: time.Minute}
	stale := dns.Record{Name: dns.Inventory, Type: "TXT", Value: "homerslab=1; media CNAME, photos CNAME", TTL: 5 * time.Minute}
	z := &zone{recs: []libdns.RR{{Name: "@", Type: "NS", Data: "ns.example.test."}, {Name: "media", Type: "CNAME", Data: media.Value},
		{Name: dns.Inventory, Type: "TXT", Data: stale.Value}}}
	b := &staleBook{recs: []dns.Record{media, stale}}
	var notes []dns.Drift
	i := manager.Issuance{Zone: "example.test.", Domain: "example.test", Drift: func(d dns.Drift) { notes = append(notes, d) }}

	z.empty = true
	if err := manager.ReconcileZone(ctx, i, z, b); !errors.Is(err, dns.ErrListingEmpty) || len(z.writes) != 0 {
		t.Errorf("on an empty listing: %v, written %v", err, z.writes)
	}
	if len(notes) != 1 || notes[0].What != dns.DriftListingLookedEmpty || notes[0].Record.Name != dns.Inventory {
		t.Errorf("the drift notes: %+v", notes)
	}
	z.empty = false
	if err := manager.ReconcileZone(ctx, i, z, b); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(z.writes, []string{"add _homerslab", "delete _homerslab"}) {
		t.Errorf("written %v, want the inventory record only", z.writes)
	}
	if !slices.ContainsFunc(z.recs, func(r libdns.RR) bool { return r.Name == dns.Inventory && r.Data == "homerslab=1; media CNAME" }) ||
		slices.ContainsFunc(z.recs, func(r libdns.RR) bool { return r.Data == stale.Value }) {
		t.Errorf("the zone: %+v", z.recs)
	}
}
