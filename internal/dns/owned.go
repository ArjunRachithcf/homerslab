package dns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/libdns/libdns"
)

// Provider is a household's DNS provider through libdns: list, add and delete records.
type Provider interface {
	libdns.RecordGetter
	libdns.RecordAppender
	libdns.RecordDeleter
}

// Record is one record HomersLab created, as the manager's database keeps it: the source of truth for what is
// HomersLab's in a zone (#79). Name is relative to the zone. (The spec's provider ID isn't kept: neither provider's
// libdns package gives one back.)
type Record struct {
	Name  string        `json:"name"`
	Type  string        `json:"type"`
	Value string        `json:"value"`
	TTL   time.Duration `json:"ttl"`
}

// Book keeps the records HomersLab created, per zone.
type Book interface {
	Records(ctx context.Context, zone string) ([]Record, error)
	Keep(ctx context.Context, zone string, r Record) error
	Forget(ctx context.Context, zone string, r Record) error
}

// The ways a change is refused.
var (
	// ErrTaken is a name that holds someone else's record.
	ErrTaken = errors.New("the name holds a record that isn't HomersLab's")
	// ErrNotOurs is a record HomersLab didn't create: it is never changed or deleted.
	ErrNotOurs = errors.New("the record isn't HomersLab's")
	// ErrNoValue is a record without its value: with libdns, a delete without one deletes every value at the name.
	ErrNoValue = errors.New("a record needs its value")
	// ErrNotDeleted is a delete the provider did nothing for: the record stays in the book, so it is found again.
	ErrNotDeleted = errors.New("the provider deleted nothing")
)

// Drift is a note for the drift report: HomersLab's record and the provider's zone no longer agree, and the manager
// put its own side right.
type Drift struct {
	Zone   string
	Record Record
	What   string
}

// DriftDeletedByHand is a record of HomersLab's that was deleted at the provider by hand: the book dropped it.
const DriftDeletedByHand = "record-deleted-at-the-provider"

// Inventory is the name, under the domain, of the one TXT record that lists HomersLab's records by name and type, so
// a restored primary finds them again (#79).
const Inventory = "_homerslab"

// Challenge is the name, under the domain, of the ACME DNS challenge's records.
const Challenge = "_acme-challenge"

// Owned is a zone as HomersLab may change it: only the records it created, which the Book lists.
type Owned struct {
	Provider Provider
	Zone     string // as libdns names it, with a trailing dot
	Domain   string // the household's domain, at or under the zone
	Book     Book
	Log      *slog.Logger // where a delete the provider did nothing for is said; none when nil
	Drift    func(Drift)  // the drift report's note for a record found deleted at the provider; none when nil
}

func (o *Owned) zone() string { return strings.ToLower(libdns.AbsoluteName("@", o.Zone)) }

// rel is a name under the domain, relative to the zone: "_acme-challenge", or "_acme-challenge.home" when the
// domain is home.<zone>.
func (o *Owned) rel(label string) string {
	return libdns.RelativeName(strings.TrimSuffix(label+"."+o.Domain, ".")+".", o.zone())
}

// Name is a label under the domain, as a Record names it.
func (o *Owned) Name(label string) string { return o.rel(label) }

func same(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}

// at lists the zone's records at a name, as the provider has them.
func (o *Owned) at(ctx context.Context, name string) ([]libdns.RR, error) {
	all, err := o.Provider.GetRecords(ctx, o.zone())
	if err != nil {
		return nil, fmt.Errorf("listing the zone %s: %w", o.zone(), err)
	}
	var out []libdns.RR
	for _, r := range all {
		if rr := r.RR(); same(rr.Name, name) {
			out = append(out, rr)
		}
	}
	return out, nil
}

// Free says whether a name holds no record at all, read from the provider's listing, never from DNS, where the
// public wildcard answers every name.
func (o *Owned) Free(ctx context.Context, name string) (bool, error) {
	there, err := o.at(ctx, name)
	return len(there) == 0, err
}

// Create adds a record and keeps it as HomersLab's. It is refused when the name holds a CNAME, or anything that
// isn't HomersLab's; only an ACME challenge's TXT records, which several clients may hold at once (RFC 8555 §8.4),
// share their name.
func (o *Owned) Create(ctx context.Context, r Record) error {
	if err := o.create(ctx, r); err != nil {
		return err
	}
	return o.inventory(ctx)
}

func (o *Owned) create(ctx context.Context, r Record) error {
	if r.Value == "" || r.Type == "" {
		return fmt.Errorf("%w: %s %s", ErrNoValue, r.Type, r.Name)
	}
	there, err := o.at(ctx, r.Name)
	if err != nil {
		return err
	}
	ours, err := o.Book.Records(ctx, o.zone())
	if err != nil {
		return err
	}
	challenge := strings.EqualFold(r.Type, "TXT") && same(r.Name, o.rel(Challenge))
	for _, rr := range there {
		mine := slices.ContainsFunc(ours, func(k Record) bool { return k.matches(rr) })
		switch {
		case strings.EqualFold(rr.Type, "CNAME"), strings.EqualFold(r.Type, "CNAME"):
			return fmt.Errorf("%w: %s holds a %s, and no other data may sit beside a CNAME", ErrTaken, r.Name, rr.Type)
		case mine && strings.EqualFold(rr.Type, r.Type) && rr.Data == r.Value:
			return nil // already there, and HomersLab's
		case !mine && !(challenge && strings.EqualFold(rr.Type, "TXT")):
			return fmt.Errorf("%w: %s holds a %s record", ErrTaken, r.Name, rr.Type)
		}
	}
	if _, err := o.Provider.AppendRecords(ctx, o.zone(), []libdns.Record{r.libdns()}); err != nil {
		return fmt.Errorf("adding %s %s: %w", r.Type, r.Name, err)
	}
	return o.Book.Keep(ctx, o.zone(), r)
}

// Delete deletes one of HomersLab's records, and refuses any other.
func (o *Owned) Delete(ctx context.Context, r Record) error {
	if err := o.delete(ctx, r); err != nil {
		return err
	}
	return o.inventory(ctx)
}

func (o *Owned) delete(ctx context.Context, r Record) error {
	if r.Value == "" || r.Type == "" {
		return fmt.Errorf("%w: %s %s", ErrNoValue, r.Type, r.Name)
	}
	ours, err := o.Book.Records(ctx, o.zone())
	if err != nil {
		return err
	}
	i := slices.IndexFunc(ours, func(k Record) bool { return k.same(r) })
	if i < 0 {
		return fmt.Errorf("%w: %s %s", ErrNotOurs, r.Type, r.Name)
	}
	// Looked up first: a provider such as RFC 2136's reports every delete as done, so only the listing says whether the
	// record was still there. Gone means someone deleted it by hand.
	if there, err := o.at(ctx, r.Name); err == nil && !slices.ContainsFunc(there, ours[i].matches) {
		return o.dropGone(ctx, ours[i])
	}
	// Any TTL: a provider may keep its own (a minimum of its own), and the record is HomersLab's all the same.
	exact := ours[i]
	exact.TTL = 0
	deleted, err := o.Provider.DeleteRecords(ctx, o.zone(), []libdns.Record{exact.libdns()})
	if err != nil {
		return fmt.Errorf("deleting %s %s: %w", r.Type, r.Name, err)
	}
	if len(deleted) == 0 {
		return o.nothingDeleted(ctx, ours[i])
	}
	return o.Book.Forget(ctx, o.zone(), ours[i])
}

// nothingDeleted handles a delete the provider did nothing for. The name is looked up again in the provider's
// listing: when the record is not there, someone deleted it by hand, so the book drops it and the drift report says so. While the
// provider still has it, or when the lookup fails, nothing is known, and the record stays in the book (ErrNotDeleted).
func (o *Owned) nothingDeleted(ctx context.Context, r Record) error {
	there, err := o.at(ctx, r.Name)
	if err != nil {
		if o.Log != nil {
			o.Log.Warn("the DNS provider deleted nothing, and the lookup failed: the record stays listed as HomersLab's", "zone", o.zone(), "name", r.Name, "type", r.Type, "err", err)
		}
		return fmt.Errorf("%w: %s %s (%v)", ErrNotDeleted, r.Type, r.Name, err)
	}
	if slices.ContainsFunc(there, r.matches) {
		if o.Log != nil {
			o.Log.Warn("the DNS provider deleted nothing: the record stays listed as HomersLab's", "zone", o.zone(), "name", r.Name, "type", r.Type)
		}
		return fmt.Errorf("%w: %s %s", ErrNotDeleted, r.Type, r.Name)
	}
	return o.dropGone(ctx, r)
}

// dropGone drops a record the provider is seen not to have from the book, and says so in the drift report.
func (o *Owned) dropGone(ctx context.Context, r Record) error {
	if err := o.Book.Forget(ctx, o.zone(), r); err != nil {
		return err
	}
	if o.Log != nil {
		o.Log.Warn("a record of HomersLab's was deleted at the provider by hand: the book dropped it", "zone", o.zone(), "name", r.Name, "type", r.Type)
	}
	if o.Drift != nil {
		o.Drift(Drift{Zone: o.zone(), Record: r, What: DriftDeletedByHand})
	}
	return nil
}

// inventory writes the one record that lists HomersLab's records, or deletes it when there are none.
func (o *Owned) inventory(ctx context.Context) error {
	ours, err := o.Book.Records(ctx, o.zone())
	if err != nil {
		return err
	}
	name := o.rel(Inventory)
	var listed []string
	var old []Record
	for _, r := range ours {
		if same(r.Name, name) && strings.EqualFold(r.Type, "TXT") {
			old = append(old, r)
			continue
		}
		listed = append(listed, r.Name+" "+strings.ToUpper(r.Type))
	}
	slices.Sort(listed)
	listed = slices.Compact(listed)
	want := ""
	if len(listed) > 0 {
		want = "homerslab=1; " + strings.Join(listed, ", ")
	}
	if len(old) == 1 && old[0].Value == want {
		return nil
	}
	if want != "" {
		if err := o.create(ctx, Record{Name: name, Type: "TXT", Value: want, TTL: 5 * time.Minute}); err != nil {
			return fmt.Errorf("the inventory record: %w", err)
		}
	}
	for _, r := range old {
		if r.Value != want {
			if err := o.delete(ctx, r); err != nil {
				return fmt.Errorf("the old inventory record: %w", err)
			}
		}
	}
	return nil
}

func (r Record) libdns() libdns.Record {
	return libdns.RR{Name: r.Name, TTL: r.TTL, Type: strings.ToUpper(r.Type), Data: r.Value}
}

func (r Record) same(o Record) bool {
	return same(r.Name, o.Name) && strings.EqualFold(r.Type, o.Type) && r.Value == o.Value
}

func (r Record) matches(rr libdns.RR) bool {
	return same(r.Name, rr.Name) && strings.EqualFold(r.Type, rr.Type) && r.Value == rr.Data
}
