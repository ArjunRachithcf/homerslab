package cli

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/manager/plan"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The link's address follows the setup listener: the address it is bound to (unless loopback), or for an
// unspecified one, the LAN's best address in the families it answers.
func TestTheLinksAddressFollowsTheListener(t *testing.T) {
	testenv.Isolate(t)
	v6LAN := func() lan.LAN { return lan.LAN{Addresses: []netip.Addr{netip.MustParseAddr("fd00:1::20")}} }
	for listen, want := range map[string]string{
		"0.0.0.0":      "", // --listen 0.0.0.0:7020 on an IPv6-only LAN: IPv4 only, so no LAN link
		"::":           "fd00:1::20",
		"192.168.1.20": "192.168.1.20",
		"fd00:1::20":   "fd00:1::20",
		"127.0.0.1":    "",
		"::1":          "",
	} {
		got := linkAddress(netip.MustParseAddr(listen), v6LAN)()
		if (want == "" && got.IsValid()) || (want != "" && got != netip.MustParseAddr(want)) {
			t.Errorf("listening on %s: the link's address = %v, want %q", listen, got, want)
		}
	}
}

// A person's name in a plan is shown inside FSI...PDI (#124), so a right-to-left name can't reorder "Adults:" before
// it or the words after it.
func TestAPlansPeopleAreIsolated(t *testing.T) {
	testenv.Isolate(t)
	var b strings.Builder
	planWords(&b, plan.Plan{Audience: plan.Audience{Default: "adults", People: []plan.Person{{Name: "محمد رضا", Role: "admin"}, {Name: "Rui", Role: "adult"}}}})
	if want := "Adults: ⁨محمد رضا⁩, ⁨Rui⁩"; !strings.Contains(b.String(), want) {
		t.Errorf("no %+q in\n%s", want, b.String())
	}
}
