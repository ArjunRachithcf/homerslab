package dns_test

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libdns/libdns"
	"golang.org/x/net/idna"

	"github.com/ArjunRachithcf/homerslab/internal/dns"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// provider is a DNS provider's zone, in memory, through libdns.
type provider struct {
	mu      sync.Mutex
	zone    string // as the provider names it; example.test. when empty
	records []libdns.RR
	writes  []string
}

func (p *provider) GetRecords(_ context.Context, zone string) ([]libdns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if want := cmp.Or(p.zone, "example.test."); zone != want {
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
	if got, err := o.Name(dns.Challenge); err != nil || got != "_acme-challenge.home" {
		t.Errorf("the challenge's name: %q %v", got, err)
	}
	if got, err := o.Name(dns.Inventory); err != nil || got != "_homerslab.home" {
		t.Errorf("the inventory's name: %q %v", got, err)
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

	// Someone deleted HomersLab's record at the provider by hand: the lookup finds it gone, so the book drops it, and
	// the drift report says so.
	var notes []dns.Drift
	o.Drift = func(d dns.Drift) { notes = append(notes, d) }
	r := dns.Record{Name: "photos", Type: "CNAME", Value: "proxy.cluster.test."}
	if err := o.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.records = slices.DeleteFunc(p.records, func(x libdns.RR) bool { return x.Name == "photos" })
	p.mu.Unlock()
	if err := o.Delete(ctx, r); err != nil {
		t.Errorf("a delete of a record gone from the provider: %v", err)
	}
	if recs, _ := b.Records(ctx, "example.test."); slices.ContainsFunc(recs, func(k dns.Record) bool { return k.Name == "photos" }) {
		t.Error("the book still lists a record the provider no longer has")
	}
	if len(notes) != 1 || notes[0].Zone != "example.test." || notes[0].Record.Name != "photos" || notes[0].What != dns.DriftDeletedByHand {
		t.Errorf("the drift report: %+v", notes)
	}
}

// stubborn is a provider whose deletes do nothing (or whose lookups fail): the record is still there, or unknown.
type stubborn struct {
	*provider
	failList atomic.Bool
}

func (s *stubborn) GetRecords(ctx context.Context, zone string) ([]libdns.Record, error) {
	if s.failList.Load() {
		return nil, errors.New("the provider is down")
	}
	return s.provider.GetRecords(ctx, zone)
}

func (s *stubborn) DeleteRecords(context.Context, string, []libdns.Record) ([]libdns.Record, error) {
	return nil, nil
}

// A record is dropped from the book only when the provider is seen not to have it: never while it is still there,
// never when the lookup fails.
func TestARecordStaysInTheBookWhileTheProviderMayHaveIt(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	r := dns.Record{Name: "photos", Type: "CNAME", Value: "proxy.cluster.test."}
	for _, tc := range []struct {
		name string
		prep func(*stubborn)
	}{
		{"the provider still has it", func(*stubborn) {}},
		{"the lookup fails", func(s *stubborn) {
			s.provider.mu.Lock()
			s.provider.records = slices.DeleteFunc(s.provider.records, func(x libdns.RR) bool { return x.Name == "photos" })
			s.provider.mu.Unlock()
			s.failList.Store(true)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, b := &stubborn{provider: household()}, &book{}
			o := &dns.Owned{Provider: s, Zone: "example.test.", Domain: "example.test", Book: b}
			var notes []dns.Drift
			o.Drift = func(d dns.Drift) { notes = append(notes, d) }
			if err := o.Create(ctx, r); err != nil {
				t.Fatal(err)
			}
			tc.prep(s)
			if err := o.Delete(ctx, r); !errors.Is(err, dns.ErrNotDeleted) {
				t.Errorf("delete: %v, want ErrNotDeleted", err)
			}
			if recs, _ := b.Records(ctx, "example.test."); !slices.ContainsFunc(recs, func(k dns.Record) bool { return k.Name == "photos" }) {
				t.Error("the book forgot a record that may still be at the provider")
			}
			if len(notes) != 0 {
				t.Errorf("a drift note without proof: %+v", notes)
			}
		})
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

// unreachable is a provider whose listing fails while it is down; its deletes are the in-memory zone's, which delete
// every value at a name when given none.
type unreachable struct {
	*provider
	down atomic.Bool
}

func (u *unreachable) GetRecords(ctx context.Context, zone string) ([]libdns.Record, error) {
	if u.down.Load() {
		return nil, errors.New("the provider is down")
	}
	return u.provider.GetRecords(ctx, zone)
}

// A record of HomersLab's that someone replaced by hand (deleted it, and added another value at the same name and
// type) leaves the book with a note once a delete removes nothing and the lookup shows its value gone. The other value
// is never deleted or changed, on any retry (#153).
func TestARecordReplacedByHandLeavesTheBook(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	for _, r := range []dns.Record{
		{Name: "photos", Type: "TXT", Value: "ours"},
		{Name: "media", Type: "CNAME", Value: "proxy.cluster.test."},
	} {
		t.Run(r.Type, func(t *testing.T) {
			u, b := &unreachable{provider: household()}, &book{}
			o := &dns.Owned{Provider: u, Zone: "example.test.", Domain: "example.test", Book: b}
			var notes []dns.Drift
			o.Drift = func(d dns.Drift) { notes = append(notes, d) }
			if err := o.Create(ctx, r); err != nil {
				t.Fatal(err)
			}
			foreign := "someone-else.test."
			if r.Type == "TXT" {
				foreign = "someone else's"
			}
			u.mu.Lock()
			for i := range u.records {
				if u.records[i].Name == r.Name {
					u.records[i].Data = foreign
				}
			}
			u.mu.Unlock()
			survives := func(when string) {
				t.Helper()
				if got := u.has(r.Name, r.Type); !slices.Equal(got, []string{foreign}) {
					t.Errorf("%s: the hand-added value is %v, want [%s]", when, got, foreign)
				}
			}
			inBook := func() bool {
				recs, _ := b.Records(ctx, "example.test.")
				return slices.ContainsFunc(recs, func(k dns.Record) bool { return k.Name == r.Name })
			}

			// While the provider can't be listed, nothing is known: the record stays, and no note is written.
			u.down.Store(true)
			if err := o.Delete(ctx, r); !errors.Is(err, dns.ErrNotDeleted) {
				t.Errorf("a delete while the listing fails: %v, want ErrNotDeleted", err)
			}
			survives("after a delete while the listing fails")
			if !inBook() || len(notes) != 0 {
				t.Errorf("without a lookup: in the book %v, notes %+v", inBook(), notes)
			}

			u.down.Store(false)
			if err := o.Delete(ctx, r); err != nil {
				t.Errorf("a delete of a record replaced by hand: %v", err)
			}
			survives("after the delete")
			if inBook() {
				t.Error("the book still lists a record whose value is gone from the provider")
			}
			if len(notes) != 1 || notes[0].What != dns.DriftReplacedByHand || notes[0].Record != r || notes[0].Zone != "example.test." {
				t.Errorf("the drift report: %+v", notes)
			}
			for range 3 {
				if err := o.Delete(ctx, r); !errors.Is(err, dns.ErrNotOurs) {
					t.Errorf("a retry: %v, want ErrNotOurs", err)
				}
				survives("after a retry")
			}
			if err := o.Create(ctx, r); !errors.Is(err, dns.ErrTaken) {
				t.Errorf("creating it again beside the hand-added value: %v, want ErrTaken", err)
			}
			survives("after creating it again")
		})
	}
}

// quoting is a provider that lists TXT values in quotes (split in two strings, with escapes), CNAME targets in capitals without their trailing dot, and
// names with a trailing dot, as some providers do, and deletes only what it is given in that form.
type quoting struct{ *provider }

func (q quoting) GetRecords(ctx context.Context, zone string) ([]libdns.Record, error) {
	recs, err := q.provider.GetRecords(ctx, zone)
	for i, r := range recs {
		rr := r.RR()
		switch rr.Type {
		case "TXT":
			esc := strings.NewReplacer("u", `\117`, ";", `\;`)                                                          // RFC 1035's escapes, \DDD and \X
			rr.Data = `"` + esc.Replace(rr.Data[:len(rr.Data)/2]) + `" "` + esc.Replace(rr.Data[len(rr.Data)/2:]) + `"` // in two strings
		case "CNAME":
			rr.Data = strings.ToUpper(strings.TrimSuffix(rr.Data, "."))
		}
		rr.Name += "."
		recs[i] = rr
	}
	return recs, err
}

func (q quoting) DeleteRecords(context.Context, string, []libdns.Record) ([]libdns.Record, error) {
	return nil, nil
}

// A provider that keeps values in its own form must not make a live record look deleted, or replaced by hand.
func TestAProviderThatRewritesValuesNeverMakesALiveRecordLookGone(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	for _, r := range []dns.Record{
		{Name: "photos", Type: "TXT", Value: "ours"},
		{Name: "media", Type: "CNAME", Value: "proxy.cluster.test."},
	} {
		t.Run(r.Type, func(t *testing.T) {
			p, b := household(), &book{}
			o := &dns.Owned{Provider: quoting{p}, Zone: "example.test.", Domain: "example.test", Book: b}
			var notes []dns.Drift
			o.Drift = func(d dns.Drift) { notes = append(notes, d) }
			if err := o.Create(ctx, r); err != nil {
				t.Fatal(err)
			}
			if r.Type == "TXT" { // (a CNAME is never created twice)
				if err := o.Create(ctx, r); err != nil {
					t.Errorf("creating it again, as the provider lists it in its own form: %v", err)
				}
			}
			if got := p.has(r.Name, r.Type); len(got) != 1 {
				t.Errorf("%s %s holds %v, want one record", r.Type, r.Name, got)
			}
			if err := o.Delete(ctx, r); !errors.Is(err, dns.ErrNotDeleted) {
				t.Errorf("delete: %v, want ErrNotDeleted", err)
			}
			if recs, _ := b.Records(ctx, "example.test."); !slices.ContainsFunc(recs, func(k dns.Record) bool { return k.Name == r.Name }) {
				t.Error("the book forgot a record that is still at the provider")
			}
			if len(notes) != 0 {
				t.Errorf("a drift note for a record that is still there: %+v", notes)
			}
		})
	}
}

// listedAs is a provider that lists names in a form of its own (in Unicode, in punycode, in capitals, with a
// trailing dot), and whose deletes do nothing: a record of HomersLab's is still there.
type listedAs struct {
	*provider
	form func(string) string
}

func (l listedAs) GetRecords(ctx context.Context, zone string) ([]libdns.Record, error) {
	recs, err := l.provider.GetRecords(ctx, zone)
	for i, r := range recs {
		rr := r.RR()
		rr.Name = l.form(rr.Name)
		recs[i] = rr
	}
	return recs, err
}

func (listedAs) DeleteRecords(context.Context, string, []libdns.Record) ([]libdns.Record, error) {
	return nil, nil
}

func unicode(name string) string {
	u, err := idna.ToUnicode(name)
	if err != nil {
		panic(err)
	}
	return u
}

// Names are compared in one form, ASCII (A-labels), wherever the domain is stored in Unicode or punycode and however
// the provider lists them: a live record of HomersLab's is still recognised as its own (#153).
func TestNamesAreComparedInTheirASCIIForm(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, zone, domain  string
		providerZone, ascii string // the zone as the provider names it, and the challenge's name in ASCII
		form                func(string) string
	}{
		{"a Unicode domain, listed in punycode", "example.test.", "bücher.example.test", "", "_acme-challenge.xn--bcher-kva", strings.Clone},
		{"a punycode domain, listed in Unicode", "example.test.", "xn--bcher-kva.example.test", "", "_acme-challenge.xn--bcher-kva", unicode},
		{"mixed case and a trailing dot", "Example.Test.", "XN--BCHER-KVA.Example.TEST.", "", "_acme-challenge.xn--bcher-kva",
			func(s string) string { return strings.ToUpper(s) + "." }},
		{"a Unicode zone, its provider's in punycode", "exämple.test.", "exämple.test", "xn--exmple-cua.test.", "_acme-challenge", strings.ToUpper},
		{"a punycode zone, listed in Unicode", "xn--exmple-cua.test.", "bücher.exämple.test", "xn--exmple-cua.test.", "_acme-challenge.xn--bcher-kva", unicode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, b := household(), &book{}
			p.zone = tc.providerZone
			o := &dns.Owned{Provider: listedAs{p, tc.form}, Zone: tc.zone, Domain: tc.domain, Book: b}
			var notes []dns.Drift
			o.Drift = func(d dns.Drift) { notes = append(notes, d) }
			name, err := o.Name(dns.Challenge)
			if err != nil || name != tc.ascii {
				t.Errorf("the challenge's name: %q %v, want %q", name, err, tc.ascii)
			}
			r := dns.Record{Name: name, Type: "TXT", Value: "digest"}
			if err := o.Create(ctx, r); err != nil {
				t.Fatal(err)
			}
			if got := p.has(tc.ascii, "TXT"); !slices.Contains(got, "digest") {
				t.Errorf("%s holds %v at the provider", tc.ascii, got)
			}
			// Created again, by its name in another form: it is already there, and HomersLab's.
			for _, again := range []string{tc.form(tc.ascii), strings.ToUpper(tc.ascii) + ".", unicode(tc.ascii)} {
				if err := o.Create(ctx, dns.Record{Name: again, Type: "TXT", Value: "digest"}); err != nil {
					t.Errorf("creating %q again: %v", again, err)
				}
				if free, err := o.Free(ctx, again); err != nil || free {
					t.Errorf("%q free: %v %v, want taken", again, free, err)
				}
			}
			if got := p.has(tc.ascii, "TXT"); len(slices.DeleteFunc(got, func(v string) bool { return v != "digest" })) != 1 {
				t.Errorf("%s holds %v: written twice", tc.ascii, p.has(tc.ascii, "TXT"))
			}
			if err := o.Create(ctx, dns.Record{Name: unicode(tc.ascii), Type: "CNAME", Value: "proxy.cluster.test."}); !errors.Is(err, dns.ErrTaken) {
				t.Errorf("a CNAME beside the challenge: %v, want ErrTaken", err)
			}
			if err := o.Delete(ctx, r); !errors.Is(err, dns.ErrNotDeleted) {
				t.Errorf("delete: %v, want ErrNotDeleted", err)
			}
			recs, _ := b.Records(ctx, cmp.Or(tc.providerZone, "example.test."))
			if !slices.ContainsFunc(recs, func(k dns.Record) bool { return k.Name == tc.ascii }) {
				t.Errorf("the book forgot a record that is still at the provider: %+v", recs)
			}
			if len(notes) != 0 {
				t.Errorf("a drift note for a record that is still there: %+v", notes)
			}
		})
	}
}

// A name that isn't valid IDNA (it fails ToASCII) is refused: never kept as it is, never compared in another form.
func TestANameThatIsntValidIDNAIsRefused(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	for _, bad := range []string{"xn--zz", "xn--a", "a..b", "a b", "photos.*", "xn--bcher-kva--"} {
		t.Run(bad, func(t *testing.T) {
			p, b := household(), &book{}
			o := &dns.Owned{Provider: p, Zone: "example.test.", Domain: "example.test", Book: b}
			r := dns.Record{Name: bad, Type: "TXT", Value: "ours"}
			if err := o.Create(ctx, r); !errors.Is(err, dns.ErrBadName) {
				t.Errorf("creating %q: %v, want ErrBadName", bad, err)
			}
			if err := o.Delete(ctx, r); !errors.Is(err, dns.ErrBadName) {
				t.Errorf("deleting %q: %v, want ErrBadName", bad, err)
			}
			if _, err := o.Free(ctx, bad); !errors.Is(err, dns.ErrBadName) {
				t.Errorf("whether %q is free: %v, want ErrBadName", bad, err)
			}

			// The household's domain, or the zone.
			for _, o := range []*dns.Owned{
				{Provider: p, Zone: "example.test.", Domain: bad + ".example.test", Book: b},
				{Provider: p, Zone: bad + ".", Domain: "example.test", Book: b},
			} {
				if err := o.Create(ctx, dns.Record{Name: "_acme-challenge", Type: "TXT", Value: "ours"}); !errors.Is(err, dns.ErrBadName) {
					t.Errorf("creating under the domain %q in the zone %q: %v, want ErrBadName", o.Domain, o.Zone, err)
				}
			}

			if len(p.writes) != 0 {
				t.Errorf("written: %v", p.writes)
			}

			// A name the provider lists that isn't valid is never at one of HomersLab's names, which are, and is never
			// touched; it doesn't stop HomersLab's own records.
			p.mu.Lock()
			p.records = append(p.records, libdns.RR{Name: bad, Type: "TXT", Data: "someone else's"})
			p.mu.Unlock()
			r = dns.Record{Name: "photos", Type: "TXT", Value: "ours"}
			if err := o.Create(ctx, r); err != nil {
				t.Errorf("creating beside a listed %q: %v", bad, err)
			}
			if err := o.Delete(ctx, r); err != nil {
				t.Errorf("deleting beside a listed %q: %v", bad, err)
			}
			if got := p.has(bad, "TXT"); !slices.Equal(got, []string{"someone else's"}) {
				t.Errorf("the record listed at %q: %v", bad, got)
			}
		})
	}
}

// A record the book kept in another form (in capitals, with a trailing dot) is still HomersLab's, and is forgotten in
// the form the book keeps it.
func TestTheBooksNamesAreReadInTheirASCIIForm(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	p, b := household(), &book{}
	p.records = append(p.records, libdns.RR{Name: "photos", Type: "TXT", Data: "ours"})
	if err := b.Keep(ctx, "example.test.", dns.Record{Name: "PHOTOS.", Type: "TXT", Value: "ours"}); err != nil {
		t.Fatal(err)
	}
	o := &dns.Owned{Provider: p, Zone: "example.test.", Domain: "example.test", Book: b}
	if err := o.Delete(ctx, dns.Record{Name: "photos", Type: "TXT", Value: "ours"}); err != nil {
		t.Errorf("deleting a record the book keeps in capitals: %v", err)
	}
	if recs, _ := b.Records(ctx, "example.test."); len(recs) != 0 {
		t.Errorf("the book still lists %+v", recs)
	}
	if got := p.has("photos", "TXT"); len(got) != 0 {
		t.Errorf("photos holds %v", got)
	}

	// Deleted by hand meanwhile: dropped from the book in the form the book keeps it.
	if err := b.Keep(ctx, "example.test.", dns.Record{Name: "Photos", Type: "TXT", Value: "ours"}); err != nil {
		t.Fatal(err)
	}
	if err := o.Delete(ctx, dns.Record{Name: "photos", Type: "TXT", Value: "ours"}); err != nil {
		t.Errorf("deleting a record deleted by hand, which the book keeps in capitals: %v", err)
	}
	if recs, _ := b.Records(ctx, "example.test."); len(recs) != 0 {
		t.Errorf("the book still lists %+v", recs)
	}
}

// Names a zone may well hold, that IDNA's rules for registering a name would refuse: a label with hyphens at its third
// and fourth places, and a domain with a right-to-left label beside _acme-challenge (#153's review).
func TestNamesIDNAWouldNotRegisterAreStillRead(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	for _, domain := range []string{"ab--home.example.test", "xn--4gbrim.example.test", "موقع.example.test"} {
		t.Run(domain, func(t *testing.T) {
			p := household()
			p.records = append(p.records, libdns.RR{Name: "r3--cdn", Type: "CNAME", Data: "cdn.example."})
			o := &dns.Owned{Provider: p, Zone: "example.test.", Domain: domain, Book: &book{}}
			name, err := o.Name(dns.Challenge)
			if err != nil {
				t.Fatal(err)
			}
			r := dns.Record{Name: name, Type: "TXT", Value: "digest"}
			if err := o.Create(ctx, r); err != nil {
				t.Errorf("the challenge under %s: %v", domain, err)
			}
			if err := o.Delete(ctx, r); err != nil {
				t.Errorf("its cleanup: %v", err)
			}
			if free, err := o.Free(ctx, "r3--cdn"); err != nil || free {
				t.Errorf("r3--cdn free: %v %v, want taken", free, err)
			}
		})
	}
}

// unicodeStore is a provider that keeps names in Unicode, lists them so, and deletes only what is given in that form.
type unicodeStore struct{ *provider }

func (u unicodeStore) AppendRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	out := make([]libdns.Record, len(recs))
	for i, r := range recs {
		rr := r.RR()
		rr.Name = unicode(rr.Name)
		out[i] = rr
	}
	return u.provider.AppendRecords(ctx, zone, out)
}

// The delete is sent in the form the provider lists the record in, which is the form its delete matches (#153's
// review).
func TestADeleteNamesTheRecordAsTheProviderListsIt(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	p, b := household(), &book{}
	o := &dns.Owned{Provider: unicodeStore{p}, Zone: "example.test.", Domain: "bücher.example.test", Book: b}
	var notes []dns.Drift
	o.Drift = func(d dns.Drift) { notes = append(notes, d) }
	r := dns.Record{Name: "_acme-challenge.xn--bcher-kva", Type: "TXT", Value: "digest"}
	if err := o.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := p.has("_acme-challenge.bücher", "TXT"); !slices.Equal(got, []string{"digest"}) {
		t.Fatalf("the provider holds %v", got)
	}
	if err := o.Delete(ctx, r); err != nil {
		t.Errorf("delete: %v", err)
	}
	if got := p.has("_acme-challenge.bücher", "TXT"); len(got) != 0 {
		t.Errorf("the challenge is still at the provider: %v", got)
	}
	if recs, _ := b.Records(ctx, "example.test."); len(recs) != 0 || len(notes) != 0 {
		t.Errorf("the book %+v, notes %+v", recs, notes)
	}
}

// At the challenge's name, which other ACME clients share, their tokens beside HomersLab's don't make a token deleted
// by hand look replaced; the note says deleted, and their tokens stay.
func TestAChallengeDeletedByHandBesideAnotherClientsIsDeletedNotReplaced(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	p, b := household(), &book{}
	o := &dns.Owned{Provider: p, Zone: "example.test.", Domain: "example.test", Book: b}
	var notes []dns.Drift
	o.Drift = func(d dns.Drift) { notes = append(notes, d) }
	r := dns.Record{Name: "_acme-challenge", Type: "TXT", Value: "digest"}
	if err := o.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.records = slices.DeleteFunc(p.records, func(x libdns.RR) bool { return x.Data == "digest" })
	p.mu.Unlock()
	if err := o.Delete(ctx, r); err != nil {
		t.Errorf("delete: %v", err)
	}
	if len(notes) != 1 || notes[0].What != dns.DriftDeletedByHand {
		t.Errorf("the drift report: %+v", notes)
	}
	if got := p.has("_acme-challenge", "TXT"); !slices.Equal(got, []string{"left-by-another-acme-client"}) {
		t.Errorf("_acme-challenge holds %v", got)
	}
}

// A name or a CNAME target that IDNA's mapping would fold into one of HomersLab's (a fullwidth letter, an ideographic
// full stop, a soft hyphen) is someone else's, at a provider that keeps names as they are typed: it is never taken for
// ours, and never deleted, on any retry (#164's review).
func TestALookAlikeIsNeverOurs(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	for _, tc := range []struct {
		what string
		ours dns.Record
		look libdns.RR
	}{
		{"a fullwidth letter in the name", dns.Record{Name: "photos", Type: "A", Value: "192.0.2.1"}, libdns.RR{Name: "\uff50hotos", Type: "A", Data: "192.0.2.1"}},
		{"an ideographic full stop in the name", dns.Record{Name: "photos.home", Type: "A", Value: "192.0.2.1"}, libdns.RR{Name: "photos\u3002home", Type: "A", Data: "192.0.2.1"}},
		{"a soft hyphen in the name", dns.Record{Name: "photos", Type: "A", Value: "192.0.2.1"}, libdns.RR{Name: "pho\u00adtos", Type: "A", Data: "192.0.2.1"}},
		{"a fullwidth letter in the target", dns.Record{Name: "media", Type: "CNAME", Value: "proxy.cluster.test."}, libdns.RR{Name: "media", Type: "CNAME", Data: "\uff50roxy.cluster.test."}},
		{"an ideographic full stop in the target", dns.Record{Name: "media", Type: "CNAME", Value: "proxy.cluster.test."}, libdns.RR{Name: "media", Type: "CNAME", Data: "proxy\u3002cluster.test."}},
		{"a soft hyphen in the target", dns.Record{Name: "media", Type: "CNAME", Value: "proxy.cluster.test."}, libdns.RR{Name: "media", Type: "CNAME", Data: "pro\u00adxy.cluster.test."}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			p, b := household(), &book{}
			o := &dns.Owned{Provider: p, Zone: "example.test.", Domain: "example.test", Book: b}
			if err := o.Create(ctx, tc.ours); err != nil {
				t.Fatal(err)
			}
			// Ours deleted by hand, and the look-alike added.
			p.mu.Lock()
			p.records = slices.DeleteFunc(p.records, func(x libdns.RR) bool { return x.Name == tc.ours.Name && x.Type == tc.ours.Type })
			p.records = append(p.records, tc.look)
			p.mu.Unlock()
			survives := func(when string) {
				t.Helper()
				if got := p.has(tc.look.Name, tc.look.Type); !slices.Equal(got, []string{tc.look.Data}) {
					t.Errorf("%s: the look-alike %q holds %q", when, tc.look.Name, got)
				}
			}
			if err := o.Delete(ctx, tc.ours); err != nil {
				t.Errorf("a delete of a record deleted by hand: %v", err)
			}
			survives("after the delete")
			for range 3 {
				if err := o.Delete(ctx, tc.ours); !errors.Is(err, dns.ErrNotOurs) {
					t.Errorf("a retry: %v, want ErrNotOurs", err)
				}
				survives("after a retry")
			}
			if recs, _ := b.Records(ctx, "example.test."); len(recs) != 0 {
				t.Errorf("the book still lists %+v", recs)
			}
		})
	}
}

// A name asked for that IDNA's mapping would fold into another is refused, as a listed one is never taken for it.
func TestANameIsReadOnlyInItsTwoForms(t *testing.T) {
	testenv.Isolate(t)
	for _, n := range []string{"\uff50hotos", "photos\u3002home", "pho\u00adtos", "B\u00dcCHER", "bu\u0308cher", "\u212aey", "xn--bcher-kva.b\u00fccher"} {
		if got, err := dns.Canonical(n); !errors.Is(err, dns.ErrBadName) {
			t.Errorf("Canonical(%q) = %q %v, want ErrBadName", n, got, err)
		}
	}
	for n, want := range map[string]string{"PHOTOS.": "photos", "b\u00fccher": "xn--bcher-kva", "XN--BCHER-KVA": "xn--bcher-kva", "_acme-challenge.b\u00fccher.": "_acme-challenge.xn--bcher-kva", "@": "@", "*.Photos": "*.photos"} {
		if got, err := dns.Canonical(n); err != nil || got != want {
			t.Errorf("Canonical(%q) = %q %v, want %q", n, got, err, want)
		}
	}
}

// A TXT value's \DDD escape is one byte: a number above 255 is refused, never wrapped onto another byte ("\367" would
// otherwise read as "o").
func TestATXTEscapeAbove255IsRefused(t *testing.T) {
	testenv.Isolate(t)
	for _, v := range []string{`"o\256"`, `"\367urs"`, `"o\999"`} {
		if got, ok := dns.Quoted(v); ok {
			t.Errorf("Quoted(%s) = %q, want it refused", v, got)
		}
	}
	if got, ok := dns.Quoted(`"o\117" "rs"`); !ok || got != "ours" {
		t.Errorf(`Quoted("o\117" "rs") = %q %v, want "ours"`, got, ok)
	}
}

// flaky is a provider whose listing comes back empty for a moment (the next few listings), or fails; its deletes are
// the in-memory zone's, or do nothing at photos (a provider that keeps the value in its own form, say).
type flaky struct {
	*provider
	empty          atomic.Int32 // how many of the next listings come back empty
	fail           atomic.Bool
	deletesNothing atomic.Bool
}

func (f *flaky) GetRecords(ctx context.Context, zone string) ([]libdns.Record, error) {
	if f.fail.Load() {
		return nil, errors.New("the provider is down")
	}
	if f.empty.Load() > 0 {
		f.empty.Add(-1)
		return nil, nil
	}
	return f.provider.GetRecords(ctx, zone)
}

func (f *flaky) DeleteRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	if f.deletesNothing.Load() && slices.ContainsFunc(recs, func(r libdns.Record) bool { return r.RR().Name == "photos" }) {
		return nil, nil
	}
	return f.provider.DeleteRecords(ctx, zone, recs)
}

// An entry leaves the book as deleted by hand only when a listing that holds the zone's other records shows it gone:
// a listing that comes back empty for a moment (a transient answer, an eventual-consistency read), or fails, proves
// nothing, so no record of HomersLab's is left at the provider untracked, and a later create of the same name isn't
// taken for someone else's (#166).
func TestAListingThatLooksEmptyNeverDropsTheBooksEntries(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	photos := dns.Record{Name: "photos", Type: "CNAME", Value: "proxy.cluster.test."}
	media := dns.Record{Name: "media", Type: "CNAME", Value: "proxy.cluster.test."}
	byHand := func(f *flaky) {
		f.provider.mu.Lock()
		f.provider.records = slices.DeleteFunc(f.provider.records, func(x libdns.RR) bool { return x.Name == "photos" })
		f.provider.mu.Unlock()
	}
	for _, tc := range []struct {
		name           string
		deletesNothing bool
		prep           func(*flaky)
		err            error  // the delete's
		inBook         bool   // photos, after the delete
		atProvider     bool   // photos, after the delete
		note           string // the one drift note, if any
	}{
		{name: "one empty listing, then a delete that deletes it", prep: func(f *flaky) { f.empty.Store(1) }},
		{name: "every listing empty for a while, and a delete that deletes nothing", deletesNothing: true,
			prep: func(f *flaky) { f.empty.Store(2) }, err: dns.ErrNotDeleted, inBook: true, atProvider: true, note: dns.DriftListingLookedEmpty},
		{name: "every listing fails", deletesNothing: true, prep: func(f *flaky) { byHand(f); f.fail.Store(true) },
			err: dns.ErrNotDeleted, inBook: true},
		{name: "deleted by hand, and a listing shows the zone without it", prep: byHand, note: dns.DriftDeletedByHand},
		{name: "deleted by hand, an empty listing, then a delete that deletes nothing and a listing without it",
			deletesNothing: true, prep: func(f *flaky) { byHand(f); f.empty.Store(1) }, note: dns.DriftDeletedByHand},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, b := &flaky{provider: household()}, &book{}
			o := &dns.Owned{Provider: f, Zone: "example.test.", Domain: "example.test", Book: b}
			var notes []dns.Drift
			o.Drift = func(d dns.Drift) { notes = append(notes, d) }
			for _, r := range []dns.Record{media, photos} {
				if err := o.Create(ctx, r); err != nil {
					t.Fatal(err)
				}
			}
			f.deletesNothing.Store(tc.deletesNothing)
			tc.prep(f)
			if err := o.Delete(ctx, photos); !errors.Is(err, tc.err) || (tc.err == nil) != (err == nil) {
				t.Errorf("delete: %v, want %v", err, tc.err)
			}
			f.empty.Store(0)
			f.fail.Store(false)
			f.deletesNothing.Store(false)

			recs, _ := b.Records(ctx, "example.test.")
			kept := func(name string) bool {
				return slices.ContainsFunc(recs, func(k dns.Record) bool { return k.Name == name })
			}
			if kept("photos") != tc.inBook || !kept("media") || !kept(dns.Inventory) {
				t.Errorf("the book after the delete: %+v", recs)
			}
			if got := len(f.has("photos", "CNAME")) > 0; got != tc.atProvider {
				t.Errorf("photos at the provider: %v, want %v", got, tc.atProvider)
			}
			// No record HomersLab wrote is left at the provider without its entry in the book.
			f.provider.mu.Lock()
			left := slices.Clone(f.provider.records)
			f.provider.mu.Unlock()
			for _, rr := range left {
				if !slices.ContainsFunc(household().records, func(x libdns.RR) bool { return x == rr }) &&
					!slices.ContainsFunc(recs, func(k dns.Record) bool { return k.Name == rr.Name && k.Type == rr.Type && k.Value == rr.Data }) {
					t.Errorf("left at the provider, untracked: %+v", rr)
				}
			}
			var want []string
			if tc.note != "" {
				want = []string{tc.note}
			}
			var got []string
			for _, n := range notes {
				if n.Zone != "example.test." || n.Record != photos {
					t.Errorf("a note about another record: %+v", n)
				}
				got = append(got, n.What)
			}
			if !slices.Equal(got, want) {
				t.Errorf("the drift notes: %v, want %v", got, want)
			}

			// Once the listing is back, the name is HomersLab's: a record still in the book is deleted (a CNAME is never
			// created twice), and the name is created again.
			if tc.inBook {
				if err := o.Delete(ctx, photos); err != nil || len(f.has("photos", "CNAME")) > 0 {
					t.Errorf("deleting photos once the listing is back: %v, at the provider %v", err, f.has("photos", "CNAME"))
				}
			}
			if err := o.Create(ctx, photos); err != nil {
				t.Errorf("creating photos again: %v", err)
			}
		})
	}
}

// emptyOnce is a provider one of whose listings comes back empty: the at-th from now (none when 0).
type emptyOnce struct {
	dns.Provider
	at atomic.Int32
}

func (e *emptyOnce) GetRecords(ctx context.Context, zone string) ([]libdns.Record, error) {
	if e.at.Load() > 0 && e.at.Add(-1) == 0 {
		return nil, nil
	}
	return e.Provider.GetRecords(ctx, zone)
}

// After an empty listing, the delete goes out in HomersLab's own form, which a provider that keeps another form doesn't
// match; once the next listing shows the record, the delete is sent again as the provider lists it, so a moment's empty
// listing doesn't fail the delete, or a create whose inventory record changes (#166's review).
func TestADeleteAfterAnEmptyListingIsSentAgainInTheProvidersForm(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	p, b := household(), &book{}
	e := &emptyOnce{Provider: unicodeStore{p}}
	o := &dns.Owned{Provider: e, Zone: "example.test.", Domain: "bücher.example.test", Book: b}
	var notes []dns.Drift
	o.Drift = func(d dns.Drift) { notes = append(notes, d) }
	r := dns.Record{Name: "_acme-challenge.xn--bcher-kva", Type: "TXT", Value: "digest"}
	if err := o.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	e.at.Store(1)
	if err := o.Delete(ctx, r); err != nil {
		t.Errorf("delete: %v", err)
	}
	if got := p.has("_acme-challenge.bücher", "TXT"); len(got) != 0 {
		t.Errorf("the challenge is still at the provider: %v", got)
	}
	if recs, _ := b.Records(ctx, "example.test."); len(recs) != 0 || len(notes) != 0 {
		t.Errorf("the book %+v, notes %+v", recs, notes)
	}

	// A create whose old inventory record's delete meets the empty listing.
	photos := dns.Record{Name: "photos.xn--bcher-kva", Type: "CNAME", Value: "proxy.cluster.test."}
	media := dns.Record{Name: "media.xn--bcher-kva", Type: "CNAME", Value: "proxy.cluster.test."}
	if err := o.Create(ctx, photos); err != nil {
		t.Fatal(err)
	}
	e.at.Store(3) // the name's, the new inventory record's, then the old one's delete's
	if err := o.Create(ctx, media); err != nil {
		t.Errorf("a create whose inventory's delete met an empty listing: %v", err)
	}
	if got := p.has("_homerslab.bücher", "TXT"); len(got) != 1 || !strings.Contains(got[0], "media") {
		t.Errorf("the inventory records: %v", got)
	}
}
