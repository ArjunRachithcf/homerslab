package dns_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libdns/libdns"

	"github.com/ArjunRachithcf/homerslab/internal/dns"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// provider is a DNS provider's zone, in memory, through libdns.
type provider struct {
	mu      sync.Mutex
	records []libdns.RR
	writes  []string
}

func (p *provider) GetRecords(_ context.Context, zone string) ([]libdns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if zone != "example.test." {
		return nil, errors.New("no such zone")
	}
	out := make([]libdns.Record, len(p.records))
	for i, r := range p.records {
		out[i] = r
	}
	return out, nil
}

func (p *provider) AppendRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range recs {
		p.records = append(p.records, r.RR())
		p.writes = append(p.writes, "add "+r.RR().Name+" "+r.RR().Type)
	}
	return recs, nil
}

// DeleteRecords deletes exact matches, as libdns says: a TTL of 0 matches any TTL, an empty value any value. It returns
// what it deleted.
func (p *provider) DeleteRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var deleted []libdns.Record
	for _, r := range recs {
		rr := r.RR()
		p.records = slices.DeleteFunc(p.records, func(x libdns.RR) bool {
			hit := x.Name == rr.Name && x.Type == rr.Type && (rr.Data == "" || x.Data == rr.Data) && (rr.TTL == 0 || x.TTL == rr.TTL)
			if hit {
				deleted = append(deleted, x)
			}
			return hit
		})
		p.writes = append(p.writes, "delete "+rr.Name+" "+rr.Type)
	}
	return deleted, nil
}

func (p *provider) has(name, typ string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, r := range p.records {
		if r.Name == name && r.Type == typ {
			out = append(out, r.Data)
		}
	}
	return out
}

// book is the manager's database's list of HomersLab's records, in memory.
type book struct {
	mu   sync.Mutex
	recs map[string][]dns.Record
}

func (b *book) Records(_ context.Context, zone string) ([]dns.Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.recs[zone]), nil
}
func (b *book) Keep(_ context.Context, zone string, r dns.Record) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.recs == nil {
		b.recs = map[string][]dns.Record{}
	}
	b.recs[zone] = append(b.recs[zone], r)
	return nil
}
func (b *book) Forget(_ context.Context, zone string, r dns.Record) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recs[zone] = slices.DeleteFunc(b.recs[zone], func(k dns.Record) bool { return k.Name == r.Name && k.Type == r.Type && k.Value == r.Value })
	return nil
}

// The household's zone as a registrar leaves it, with mail records and the public path's wildcard.
func household() *provider {
	return &provider{records: []libdns.RR{
		{Name: "@", Type: "MX", Data: "10 mx.mail.example."},
		{Name: "@", Type: "TXT", Data: "v=spf1 include:_spf.mail.example ~all"},
		{Name: "_dmarc", Type: "TXT", Data: "v=DMARC1; p=quarantine"},
		{Name: "*", Type: "CNAME", Data: "proxy.cluster.test."},
		{Name: "www", Type: "CNAME", Data: "example.test."},
		{Name: "_acme-challenge", Type: "TXT", Data: "left-by-another-acme-client"},
	}}
}

// HomersLab touches only the records it created, which the manager's database lists, and one inventory TXT record;
// nothing is ever written beside a CNAME (#79).
func TestOnlyHomersLabsOwnRecordsAreChanged(t *testing.T) {
	testenv.Isolate(t)
	p, b := household(), &book{}
	o := &dns.Owned{Provider: p, Zone: "example.test.", Domain: "example.test", Book: b}
	ctx := context.Background()

	challenge := dns.Record{Name: "_acme-challenge", Type: "TXT", Value: "token-digest-1"}
	if err := o.Create(ctx, challenge); err != nil {
		t.Fatalf("the challenge's TXT, beside another ACME client's: %v", err)
	}
	if got := p.has("_acme-challenge", "TXT"); !slices.Contains(got, "token-digest-1") || !slices.Contains(got, "left-by-another-acme-client") {
		t.Errorf("_acme-challenge holds %v", got)
	}
	if inv := p.has("_homerslab", "TXT"); len(inv) != 1 || !strings.Contains(inv[0], "_acme-challenge TXT") {
		t.Errorf("the inventory record is %v, want one listing the challenge", inv)
	}

	for _, r := range []dns.Record{
		{Name: "www", Type: "TXT", Value: "beside a CNAME"},
		{Name: "photos", Type: "CNAME", Value: "proxy.cluster.test."}, // free: the wildcard doesn't count, a CNAME at www would
		{Name: "_dmarc", Type: "TXT", Value: "v=DMARC1; p=none"},
		{Name: "@", Type: "TXT", Value: "homerslab"},
	} {
		err := o.Create(ctx, r)
		if r.Name == "photos" {
			if err != nil {
				t.Errorf("a free name: %v", err)
			}
			continue
		}
		if !errors.Is(err, dns.ErrTaken) {
			t.Errorf("creating %s %s beside someone else's records: %v, want ErrTaken", r.Type, r.Name, err)
		}
	}
	if err := o.Create(ctx, dns.Record{Name: "photos", Type: "TXT", Value: "beside our own CNAME"}); !errors.Is(err, dns.ErrTaken) {
		t.Errorf("a TXT beside HomersLab's own CNAME: %v, want ErrTaken", err)
	}

	for _, r := range []dns.Record{
		{Name: "_acme-challenge", Type: "TXT", Value: "left-by-another-acme-client"},
		{Name: "@", Type: "MX", Value: "10 mx.mail.example."},
		{Name: "*", Type: "CNAME", Value: "proxy.cluster.test."},
	} {
		if err := o.Delete(ctx, r); !errors.Is(err, dns.ErrNotOurs) {
			t.Errorf("deleting someone else's %s %s: %v, want ErrNotOurs", r.Type, r.Name, err)
		}
	}
	for _, r := range []dns.Record{challenge, {Name: "photos", Type: "CNAME", Value: "proxy.cluster.test."}} {
		if err := o.Delete(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if inv := p.has("_homerslab", "TXT"); len(inv) != 0 {
		t.Errorf("the inventory record stayed with nothing to list: %v", inv)
	}
	for _, want := range household().records {
		if len(p.has(want.Name, want.Type)) == 0 {
			t.Errorf("the household's %s %s is gone", want.Type, want.Name)
		}
	}
	if len(p.records) != len(household().records) {
		t.Errorf("the zone ends with %v", p.records)
	}
}

// Whether a name is free is read from the provider's listing: the public wildcard answers every name in DNS (#79).
func TestAFreeNameIsReadFromTheProvidersListing(t *testing.T) {
	testenv.Isolate(t)
	o := &dns.Owned{Provider: household(), Zone: "example.test.", Domain: "example.test", Book: &book{}}
	ctx := context.Background()
	for name, want := range map[string]bool{"photos": true, "www": false, "_dmarc": false, "@": false} {
		if got, err := o.Free(ctx, name); err != nil || got != want {
			t.Errorf("%s free: %v %v, want %v", name, got, err, want)
		}
	}
}

// A domain under its zone (home.example.test in example.test) keeps its records under its own name.
func TestRecordsOfADomainUnderItsZone(t *testing.T) {
	testenv.Isolate(t)
	o := &dns.Owned{Provider: household(), Zone: "example.test.", Domain: "home.example.test", Book: &book{}}
	if got := o.Name(dns.Challenge); got != "_acme-challenge.home" {
		t.Errorf("the challenge's name: %q", got)
	}
	if got := o.Name(dns.Inventory); got != "_homerslab.home" {
		t.Errorf("the inventory's name: %q", got)
	}
}

func TestTheProvidersSettingsHoldNoSecret(t *testing.T) {
	testenv.Isolate(t)
	s, err := dns.Settings{Provider: "RFC2136", Server: "192.0.2.53", KeyName: "HomersLab.", KeyAlgorithm: ""}.Check()
	if err != nil || s.Server != "192.0.2.53:53" || s.KeyName != "homerslab." || s.KeyAlgorithm != "hmac-sha256" {
		t.Errorf("tidied: %+v %v", s, err)
	}
	if s.Where() != "the DNS server 192.0.2.53:53 (RFC 2136), as the key homerslab." {
		t.Errorf("where: %q", s.Where())
	}
	var inv *dns.Invalid
	if _, err := (dns.Settings{Provider: "rfc2136", Server: "x:99999", KeyName: "a b", KeyAlgorithm: "hmac-md5"}).Check(); !errors.As(err, &inv) || len(inv.Fields) != 3 {
		t.Errorf("bad RFC 2136 settings: %v", err)
	}
	if _, err := (dns.Settings{Provider: "route53"}).Check(); !errors.As(err, &inv) {
		t.Errorf("an unknown provider: %v", err)
	}
	if s, err := (dns.Settings{Provider: "infomaniak", Server: "ignored"}).Check(); err != nil || s.Server != "" {
		t.Errorf("Infomaniak: %+v %v", s, err)
	}
	if _, err := dns.Open(dns.Settings{Provider: "infomaniak"}, " "); err == nil {
		t.Error("opened a provider without a token")
	}
}

// A delete never orphans a record: it matches any TTL, never an empty value (which would delete every value at the
// name), and a record the provider didn't delete stays in the book (#16's review).
func TestADeleteNeverOrphansARecord(t *testing.T) {
	testenv.Isolate(t)
	p, b := household(), &book{}
	o := &dns.Owned{Provider: p, Zone: "example.test.", Domain: "example.test", Book: b}
	ctx := context.Background()
	if err := o.Create(ctx, dns.Record{Name: "_acme-challenge", Type: "TXT", Value: "digest", TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	// The provider keeps its own TTL: 300 seconds where HomersLab asked for 60 (Infomaniak's minimum, say).
	p.mu.Lock()
	for i := range p.records {
		p.records[i].TTL = 5 * time.Minute
	}
	p.mu.Unlock()
	if err := o.Delete(ctx, dns.Record{Name: "_acme-challenge", Type: "TXT", Value: "digest"}); err != nil {
		t.Fatalf("deleting the challenge whose TTL the provider changed: %v", err)
	}
	if got := p.has("_acme-challenge", "TXT"); slices.Contains(got, "digest") || !slices.Contains(got, "left-by-another-acme-client") {
		t.Errorf("_acme-challenge holds %v after the delete", got)
	}

	for _, r := range []dns.Record{{Name: "_acme-challenge", Type: "TXT"}, {Name: "photos", Type: "CNAME"}} {
		if err := o.Create(ctx, r); !errors.Is(err, dns.ErrNoValue) {
			t.Errorf("creating %s %s without a value: %v, want ErrNoValue", r.Type, r.Name, err)
		}
		if err := o.Delete(ctx, r); !errors.Is(err, dns.ErrNoValue) {
			t.Errorf("deleting %s %s without a value: %v, want ErrNoValue", r.Type, r.Name, err)
		}
	}
	if got := p.has("_acme-challenge", "TXT"); !slices.Contains(got, "left-by-another-acme-client") {
		t.Error("another client's challenge record is gone")
	}

	// Someone deleted HomersLab's record at the provider: nothing was deleted, and the book still lists it.
	r := dns.Record{Name: "photos", Type: "CNAME", Value: "proxy.cluster.test."}
	if err := o.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.records = slices.DeleteFunc(p.records, func(x libdns.RR) bool { return x.Name == "photos" })
	p.mu.Unlock()
	if err := o.Delete(ctx, r); !errors.Is(err, dns.ErrNotDeleted) {
		t.Errorf("a delete the provider did nothing for: %v, want ErrNotDeleted", err)
	}
	if recs, _ := b.Records(ctx, "example.test."); !slices.ContainsFunc(recs, func(k dns.Record) bool { return k.Name == "photos" }) {
		t.Error("the book forgot a record the provider never deleted")
	}
}

// Only the challenge's own name shares with other ACME clients: a look-alike name is taken like any other.
func TestOnlyTheChallengesOwnNameIsShared(t *testing.T) {
	testenv.Isolate(t)
	p := household()
	p.records = append(p.records, libdns.RR{Name: "_acme-challengex", Type: "TXT", Data: "someone else's"},
		libdns.RR{Name: "_acme-challenge.photos", Type: "TXT", Data: "someone else's"})
	o := &dns.Owned{Provider: p, Zone: "example.test.", Domain: "example.test", Book: &book{}}
	for _, name := range []string{"_acme-challengex", "_acme-challenge.photos"} {
		if err := o.Create(context.Background(), dns.Record{Name: name, Type: "TXT", Value: "ours"}); !errors.Is(err, dns.ErrTaken) {
			t.Errorf("a TXT at %s beside someone else's: %v, want ErrTaken", name, err)
		}
	}
}
