package lan_test

import (
	"net/netip"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

func TestTheLANIsWhereTheDefaultRouteGoes(t *testing.T) {
	testenv.Isolate(t)
	proc := fstest.MapFS{
		"net/route": {Data: []byte("Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
			"enp5s0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
			"enp5s0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n" +
			"docker0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n" +
			"wt0\t00004064\t00000000\t0001\t0\t0\t0\t0000C0FF\t0\t0\t0\n")},
		"net/ipv6_route": {Data: []byte(
			"00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000064 00000001 00000000 00000003 wlp4s0\n" +
				"fe800000000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001 enp5s0\n" +
				"00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200 lo\n")},
	}
	if got, want := lan.DefaultRouteInterfaces(proc), []string{"enp5s0", "wlp4s0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("interfaces = %v, want %v", got, want)
	}
	if got := lan.DefaultRouteInterfaces(fstest.MapFS{}); got != nil {
		t.Errorf("with no routes: %v", got)
	}
}

func TestOnlyThisMachineAndTheLANAreFromHere(t *testing.T) {
	testenv.Isolate(t)
	lans := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("fe80::/64")}
	for addr, want := range map[string]bool{
		"127.0.0.1": true, "127.8.9.1": true, "::1": true, "::ffff:127.0.0.1": true,
		"192.168.1.42": true, "::ffff:192.168.1.42": true, "fe80::1": true,
		"192.168.2.1": false, "172.17.0.2": false, "100.64.0.7": false, "203.0.113.9": false, "2001:db8::1": false,
	} {
		if got := lan.FromHere(netip.MustParseAddr(addr), lans); got != want {
			t.Errorf("FromHere(%s) = %v, want %v", addr, got, want)
		}
	}
}
