package cli

import (
	"net/netip"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/lan"
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
