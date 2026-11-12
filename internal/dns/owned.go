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
	// ErrBadName is a name that isn't valid IDNA: it is refused, never compared in another form (#153).
	ErrBadName = errors.New("not a valid domain name")

	// ErrListingEmpty is a listing of a zone in which the provider listed nothing: it proves nothing, so the change is
	// refused, and may be tried again later (#166, #171).
	ErrListingEmpty = errors.New("the provider's listing of the zone was empty")
)

// Drift is a note for the drift report: HomersLab's record and the provider's zone no longer agree, and the manager
// put its own side right; or, for DriftListingLookedEmpty, the provider's listing couldn't say, the book was left as it
// is, and a record that was asked for (not yet HomersLab's, it may be) wasn't written.
type Drift struct {
	Zone   string
	Record Record
	What   string
}

// DriftDeletedByHand is a record of HomersLab's that was deleted at the provider by hand: the book dropped it.
const DriftDeletedByHand = "record-deleted-at-the-provider"

// DriftReplacedByHand is a record of HomersLab's that the provider no longer has, with another value at its name and
// type: someone replaced it by hand. The book dropped it, and the other value is left as it is (#153).
const DriftReplacedByHand = "record-replaced-at-the-provider"

// DriftListingLookedEmpty is a record the provider's listing left out along with every other record of the zone. An
// empty listing proves nothing (a moment's answer, an eventual-consistency read): after a delete the provider did
// nothing for, the book kept the record (#166); before a create, the record wasn't written (#171).
const DriftListingLookedEmpty = "provider-listing-looked-empty"

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
	Drift    func(Drift)  // the drift report's note for a record found deleted or replaced at the provider, or kept through an empty listing; none when nil
}

// edit is a zone being changed, its names in their one form (Canonical): the zone and the domain, the names asked for,
// the book's and the provider's listing, each converted as it comes in (#153).
type edit struct {
	*Owned
	zone   string // with a trailing dot, as libdns and the book name it
	domain string
}

func (o *Owned) edit() (*edit, error) {
	zone, err := Canonical(o.Zone)
	if err != nil {
		return nil, fmt.Errorf("the zone: %w", err)
	}
	domain, err := Canonical(o.Domain)
	if err != nil {
		return nil, fmt.Errorf("the domain: %w", err)
	}
	if strings.Contains(zone+"."+domain, "*") || zone == "@" || domain == "@" || domain != zone && !strings.HasSuffix(domain, "."+zone) {
		return nil, fmt.Errorf("%w: the domain %s in the zone %s", ErrBadName, domain, zone)
	}
	return &edit{Owned: o, zone: zone + ".", domain: domain}, nil
}

// rel is a name under the domain, relative to the zone: "_acme-challenge", or "_acme-challenge.home" when the
// domain is home.<zone>.
func (e *edit) rel(label string) string {
	return libdns.RelativeName(label+"."+e.domain+".", e.zone)
}

// Name is a label under the domain, as a Record names it. It fails when the zone or the domain isn't a valid name.
func (o *Owned) Name(label string) (string, error) {
	e, err := o.edit()
	if err != nil {
		return "", err
	}
	return e.rel(label), nil
}

// Names are the zone, with its trailing dot as libdns names it, and the domain, in their one form (Canonical): what a
// caller reads the zone's names against, never the raw fields (#165). It fails when either isn't a valid name.
func (o *Owned) Names() (zone, domain string, err error) {
	e, err := o.edit()
	if err != nil {
		return "", "", err
	}
	return e.zone, e.domain, nil
}

// canonical is a record as it is asked for, its name in its one form.
func (r Record) canonical() (Record, error) {
	n, err := Canonical(r.Name)
	r.Name = n
	return r, err
}

// look lists the zone's records at a name (in its one form): with their names in that form, and as the provider listed
// them (raw), which is the form its deletes match; and whether the provider listed nothing at all in the zone. A name
// the provider lists that isn't valid is never at the name: HomersLab's own names are valid, and a name is never
// compared in another form. A zone is never empty, so a listing that is proves nothing: it is read like a failed one
// wherever it would drop a record from the book (#166), and refused before a write (listed, #171).
func (e *edit) look(ctx context.Context, name string) (there, raw []libdns.RR, empty bool, err error) {
	all, err := e.Provider.GetRecords(ctx, e.zone)
	if err != nil {
		return nil, nil, false, fmt.Errorf("listing the zone %s: %w", e.zone, err)
	}
	for _, r := range all {
		rr := r.RR()
		n, err := Canonical(rr.Name)
		if err != nil || n != name {
			continue
		}
		raw = append(raw, rr)
		rr.Name = n
		there = append(there, rr)
	}
	return there, raw, len(all) == 0, nil
}

// listed is look, for a write: a listing in which the provider listed nothing is refused (ErrListingEmpty), never read
// as a free name. A zone holds its apex's SOA and NS, which RFC 2136's zone transfer lists (BIND); that Infomaniak's
// API lists the apex too isn't verified. Either way a zone is never empty, so such a listing is a moment's answer, and
// the name may hold someone else's record; that holds for a zone HomersLab has never written to as well (#171).
func (e *edit) listed(ctx context.Context, name string) ([]libdns.RR, error) {
	there, _, empty, err := e.look(ctx, name)
	if err == nil && empty {
		return nil, fmt.Errorf("%w: %s", ErrListingEmpty, e.zone)
	}
	return there, err
}

// ours is the book's records for the zone: as it keeps them, and with their names in their one form.
func (e *edit) ours(ctx context.Context) (canon, kept []Record, err error) {
	kept, err = e.Book.Records(ctx, e.zone)
	if err != nil {
		return nil, nil, err
	}
	canon = make([]Record, len(kept))
	for i, r := range kept {
		if canon[i], err = r.canonical(); err != nil {
			return nil, nil, fmt.Errorf("the book's record: %w", err)
		}
	}
	return canon, kept, nil
}

// Free says whether a name holds no record at all, read from the provider's listing, never from DNS, where the
// public wildcard answers every name. A listing in which the provider listed nothing can't say (ErrListingEmpty).
func (o *Owned) Free(ctx context.Context, name string) (bool, error) {
	e, err := o.edit()
	if err != nil {
		return false, err
	}
	if name, err = Canonical(name); err != nil {
		return false, err
	}
	there, err := e.listed(ctx, name)
	return err == nil && len(there) == 0, err
}

// Create adds a record and keeps it as HomersLab's; a record that is already there and HomersLab's is left as it is.
// It is refused when the name holds a CNAME, or anything that isn't HomersLab's; only an ACME challenge's TXT records,
// which several clients may hold at once (RFC 8555 §8.4), share their name. A listing in which the provider listed
// nothing is refused too (ErrListingEmpty), with a drift note, and the record isn't written. When only the inventory
// record's listing comes back empty, the record is written and kept, the inventory isn't changed, and Create fails all
// the same, as for a listing that fails: trying again is a no-op for the record, and writes the inventory.
func (o *Owned) Create(ctx context.Context, r Record) error {
	e, err := o.edit()
	if err != nil {
		return err
	}
	if r, err = r.canonical(); err != nil {
		return err
	}
	if err := e.create(ctx, r); err != nil {
		return err
	}
	return e.inventory(ctx)
}

func (e *edit) create(ctx context.Context, r Record) error {
	if r.Value == "" || r.Type == "" {
		return fmt.Errorf("%w: %s %s", ErrNoValue, r.Type, r.Name)
	}
	there, err := e.listed(ctx, r.Name)
	if errors.Is(err, ErrListingEmpty) {
		if e.Log != nil {
			e.Log.Warn("the DNS provider listed nothing in the zone: that isn't taken as the name being free, and nothing was written", "zone", e.zone, "name", r.Name, "type", r.Type)
		}
		e.note(r, DriftListingLookedEmpty)
	}
	if err != nil {
		return err
	}
	ours, _, err := e.ours(ctx)
	if err != nil {
		return err
	}
	// Already there, and HomersLab's: nothing to write, a CNAME included.
	if slices.ContainsFunc(there, func(rr libdns.RR) bool {
		return r.matches(rr) && slices.ContainsFunc(ours, func(k Record) bool { return k.matches(rr) })
	}) {
		return nil
	}
	challenge := strings.EqualFold(r.Type, "TXT") && r.Name == e.rel(Challenge)
	for _, rr := range there {
		mine := slices.ContainsFunc(ours, func(k Record) bool { return k.matches(rr) })
		switch {
		case strings.EqualFold(rr.Type, "CNAME"), strings.EqualFold(r.Type, "CNAME"):
			return fmt.Errorf("%w: %s holds a %s, and no other data may sit beside a CNAME", ErrTaken, r.Name, rr.Type)
		case !mine && !(challenge && strings.EqualFold(rr.Type, "TXT")):
			return fmt.Errorf("%w: %s holds a %s record", ErrTaken, r.Name, rr.Type)
		}
	}
	if _, err := e.Provider.AppendRecords(ctx, e.zone, []libdns.Record{r.libdns()}); err != nil {
		return fmt.Errorf("adding %s %s: %w", r.Type, r.Name, err)
	}
	return e.Book.Keep(ctx, e.zone, r)
}

// Delete deletes one of HomersLab's records, and refuses any other. Trying again a delete whose inventory update failed
// is a success that writes the inventory, when the book's inventory still lists the record (#176).
func (o *Owned) Delete(ctx context.Context, r Record) error {
	e, err := o.edit()
	if err != nil {
		return err
	}
	if r, err = r.canonical(); err != nil {
		return err
	}
	if err := e.delete(ctx, r); err != nil {
		return err
	}
	return e.inventory(ctx)
}

func (e *edit) delete(ctx context.Context, r Record) error {
	if r.Value == "" || r.Type == "" {
		return fmt.Errorf("%w: %s %s", ErrNoValue, r.Type, r.Name)
	}
	ours, kept, err := e.ours(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(ours, func(k Record) bool { return k.same(r) })
	if i < 0 {
		// A delete that finished but whose inventory update failed leaves the inventory listing a record the book no
		// longer holds. Trying again is then a success that puts the inventory right (Delete does, after this): the
		// book shows we deleted it. Nothing but the inventory is written (#176).
		if e.deletedByUs(ours, r) {
			return nil
		}
		return fmt.Errorf("%w: %s %s", ErrNotOurs, r.Type, r.Name)
	}
	// Looked up first: a provider such as RFC 2136's reports every delete as done, so only the listing says whether the
	// record was still there. Gone, from a listing that holds the zone's other records, means someone deleted it by
	// hand. An empty listing, like a failed one, says nothing: the record is deleted as HomersLab wrote it, and never
	// left at the provider untracked (#166).
	there, raw, empty, err := e.look(ctx, r.Name)
	if err == nil && empty && e.Log != nil {
		e.Log.Warn("the DNS provider listed nothing in the zone: that isn't taken as the record deleted by hand", "zone", e.zone, "name", r.Name, "type", r.Type)
	}
	known := err == nil && !empty
	if known && !slices.ContainsFunc(there, ours[i].sameKind) {
		return e.drop(ctx, ours[i], kept[i], DriftDeletedByHand)
	}
	// The record as the provider lists it (its name in Unicode, its value in quotes, it may be), which is what its
	// delete matches; HomersLab's own form when the listing failed. Any TTL: a provider may keep its own (a minimum of
	// its own), and the record is HomersLab's all the same. Never without its value, which would delete every value at
	// the name.
	exact := ours[i].libdns().RR()
	if j := slices.IndexFunc(there, ours[i].matches); known && j >= 0 && raw[j].Data != "" {
		exact = raw[j]
	}
	exact.TTL = 0
	deleted, err := e.Provider.DeleteRecords(ctx, e.zone, []libdns.Record{exact})
	if err != nil {
		return fmt.Errorf("deleting %s %s: %w", r.Type, r.Name, err)
	}
	if len(deleted) == 0 {
		return e.nothingDeleted(ctx, ours, i, kept[i], exact)
	}
	return e.Book.Forget(ctx, e.zone, kept[i])
}

// nothingDeleted handles a delete the provider did nothing for. The name is looked up again in the provider's
// listing:
//   - our value is still there (in the provider's own form, it may be): when that form isn't the one the delete was
//     sent in (HomersLab's own, after a listing that failed or came back empty), the delete is sent once more in it;
//     otherwise, or when that deletes nothing either, the record stays in the book (ErrNotDeleted);
//   - the lookup fails: nothing is known, and the record stays too;
//   - the provider lists nothing in the zone, which holds at least this record: nothing is known either, the record
//     stays, and the drift report says the listing looked empty (#166);
//   - nothing of that name and type is left, or only HomersLab's other values (or, at the challenge's name, which other
//     ACME clients share, anyone's): someone deleted it by hand;
//   - someone else's value of that name and type is: someone replaced it by hand (#153).
//
// The last two drop it from the book, and the drift report says so. The provider's other records are never touched.
func (e *edit) nothingDeleted(ctx context.Context, ours []Record, i int, kept Record, sent libdns.RR) error {
	r := ours[i]
	there, raw, empty, err := e.look(ctx, r.Name)
	foreign := func(rr libdns.RR) bool {
		return r.sameKind(rr) && !slices.ContainsFunc(ours, func(k Record) bool { return k.matches(rr) })
	}
	shared := strings.EqualFold(r.Type, "TXT") && r.Name == e.rel(Challenge)
	switch {
	case err != nil:
		if e.Log != nil {
			e.Log.Warn("the DNS provider deleted nothing, and the lookup failed: the record stays listed as HomersLab's", "zone", e.zone, "name", r.Name, "type", r.Type, "err", err)
		}
		return fmt.Errorf("%w: %s %s (%v)", ErrNotDeleted, r.Type, r.Name, err)
	case empty:
		if e.Log != nil {
			e.Log.Warn("the DNS provider deleted nothing, and listed nothing in the zone: the record stays listed as HomersLab's", "zone", e.zone, "name", r.Name, "type", r.Type)
		}
		e.note(r, DriftListingLookedEmpty)
		return fmt.Errorf("%w: %s %s (%v)", ErrNotDeleted, r.Type, r.Name, ErrListingEmpty)
	case slices.ContainsFunc(there, r.matches):
		exact := raw[slices.IndexFunc(there, r.matches)]
		if exact.TTL = 0; exact.Data != "" && exact != sent {
			deleted, err := e.Provider.DeleteRecords(ctx, e.zone, []libdns.Record{exact})
			if err != nil {
				return fmt.Errorf("deleting %s %s: %w", r.Type, r.Name, err)
			}
			if len(deleted) > 0 {
				return e.Book.Forget(ctx, e.zone, kept)
			}
		}
		if e.Log != nil {
			e.Log.Warn("the DNS provider deleted nothing: the record stays listed as HomersLab's", "zone", e.zone, "name", r.Name, "type", r.Type)
		}
		return fmt.Errorf("%w: %s %s", ErrNotDeleted, r.Type, r.Name)
	case !shared && slices.ContainsFunc(there, foreign):
		return e.drop(ctx, r, kept, DriftReplacedByHand)
	default:
		return e.drop(ctx, r, kept, DriftDeletedByHand)
	}
}

// drop drops a record the provider is seen not to have from the book (as the book keeps it), and says so in the drift
// report.
func (e *edit) drop(ctx context.Context, r, kept Record, what string) error {
	if err := e.Book.Forget(ctx, e.zone, kept); err != nil {
		return err
	}
	if e.Log != nil {
		how := "deleted"
		if what == DriftReplacedByHand {
			how = "replaced by another value"
		}
		e.Log.Warn("a record of HomersLab's was "+how+" at the provider by hand: the book dropped it", "zone", e.zone, "name", r.Name, "type", r.Type)
	}
	e.note(r, what)
	return nil
}

// note is a note for the drift report, when there is one.
func (e *edit) note(r Record, what string) {
	if e.Drift != nil {
		e.Drift(Drift{Zone: e.zone, Record: r, What: what})
	}
}

// deletedByUs says whether the book's inventory record lists a name and type that no record of the book has any more:
// a record HomersLab wrote and deleted, whose inventory update didn't finish (#176). A record at that name and type that
// someone else holds is no record of the book's, and nothing is changed for it: only the inventory is rewritten.
func (e *edit) deletedByUs(ours []Record, r Record) bool {
	if r.Name == e.rel(Inventory) {
		return false
	}
	key := r.Name + " " + strings.ToUpper(r.Type)
	if slices.ContainsFunc(ours, func(k Record) bool { return k.Name == r.Name && strings.EqualFold(k.Type, r.Type) }) {
		return false
	}
	for _, k := range ours {
		if k.Name == e.rel(Inventory) && strings.EqualFold(k.Type, "TXT") && slices.Contains(inventoried(k.Value), key) {
			return true
		}
	}
	return false
}

// inventoried is the "name TYPE" entries an inventory record's value lists.
func inventoried(value string) []string {
	list, ok := strings.CutPrefix(value, "homerslab=1; ")
	if !ok {
		return nil
	}
	return strings.Split(list, ", ")
}

// Reconcile rewrites the inventory record from the book, without any other write: for a create or delete whose
// inventory update failed or met an empty listing, the periodic check calls it (#176). It fails as any write does,
// ErrListingEmpty included, and is a no-op when the inventory is right.
func (o *Owned) Reconcile(ctx context.Context) error {
	e, err := o.edit()
	if err != nil {
		return err
	}
	return e.inventory(ctx)
}

// inventory writes the one record that lists HomersLab's records, or deletes it when there are none.
func (e *edit) inventory(ctx context.Context) error {
	ours, _, err := e.ours(ctx)
	if err != nil {
		return err
	}
	name := e.rel(Inventory)
	var listed []string
	var old []Record
	for _, r := range ours {
		if r.Name == name && strings.EqualFold(r.Type, "TXT") {
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
		if err := e.create(ctx, Record{Name: name, Type: "TXT", Value: want, TTL: 5 * time.Minute}); err != nil {
			return fmt.Errorf("the inventory record: %w", err)
		}
	}
	for _, r := range old {
		if r.Value != want {
			if err := e.delete(ctx, r); err != nil {
				return fmt.Errorf("the old inventory record: %w", err)
			}
		}
	}
	return nil
}

func (r Record) libdns() libdns.Record {
	return libdns.RR{Name: r.Name, TTL: r.TTL, Type: strings.ToUpper(r.Type), Data: r.Value}
}

// The comparisons below take names in their one form (Canonical), and compare them as they are.

func (r Record) same(o Record) bool {
	return r.Name == o.Name && strings.EqualFold(r.Type, o.Type) && r.Value == o.Value
}

// sameKind is a record of the same name and type, whatever its value.
func (r Record) sameKind(rr libdns.RR) bool {
	return r.Name == rr.Name && strings.EqualFold(r.Type, rr.Type)
}

// matches is a record of the same name and type with its value, in the provider's own form, it may be.
func (r Record) matches(rr libdns.RR) bool {
	return r.sameKind(rr) && sameValue(r.Type, r.Value, rr.Data)
}
