package firewall_test

import (
	"testing"

	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// The namespace tests' IPv6 probes need every link's link-local address usable, as neighbour discovery sends from it
// (#247). settled reads that from `ip -o link show` and `ip -6 -o addr show`, as a namespace lists them.
func TestANamespaceIsSettledOnlyOnceEveryLinkHasAUsableLinkLocalAddress(t *testing.T) {
	testenv.Isolate(t)
	const (
		lo     = "1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 qdisc noqueue state UNKNOWN mode DEFAULT group default qlen 1000\\    link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00\n"
		eth0   = "2: eth0@if7: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP mode DEFAULT group default qlen 1000\\    link/ether 3e:a2:45:e3:3c:8b brd ff:ff:ff:ff:ff:ff link-netnsid 0\n"
		noCarr = "3: br-x: <NO-CARRIER,BROADCAST,MULTICAST,UP> mtu 1500 qdisc noqueue state DOWN mode DEFAULT group default qlen 1000\\    link/ether 6a:11:22:33:44:55 brd ff:ff:ff:ff:ff:ff\n"
		down   = "4: br-y: <BROADCAST,MULTICAST> mtu 1500 qdisc noop state DOWN mode DEFAULT group default qlen 1000\\    link/ether 6a:11:22:33:44:56 brd ff:ff:ff:ff:ff:ff\n"
		tun    = "5: tun0: <POINTOPOINT,MULTICAST,NOARP,UP,LOWER_UP> mtu 1500 qdisc fq_codel state UNKNOWN mode DEFAULT group default qlen 500\\    link/none \n"

		loAddr     = "1: lo    inet6 ::1/128 scope host noprefixroute \\       valid_lft forever preferred_lft forever\n"
		global     = "2: eth0    inet6 2001:db8:77::50/64 scope global nodad \\       valid_lft forever preferred_lft forever\n"
		ll         = "2: eth0    inet6 fe80::3ca2:45ff:fee3:3c8b/64 scope link proto kernel_ll \\       valid_lft forever preferred_lft forever\n"
		llTent     = "2: eth0    inet6 fe80::3ca2:45ff:fee3:3c8b/64 scope link tentative proto kernel_ll \\       valid_lft forever preferred_lft forever\n"
		llFailed   = "2: eth0    inet6 fe80::3ca2:45ff:fee3:3c8b/64 scope link tentative dadfailed proto kernel_ll \\       valid_lft forever preferred_lft forever\n"
		globalTent = "2: eth0    inet6 2001:db8:77::51/64 scope global tentative \\       valid_lft forever preferred_lft forever\n"
	)
	for _, c := range []struct {
		why            string
		links, addrs   string
		settled, fails bool
	}{
		{"a link with carrier and a usable link-local address", lo + eth0, loAddr + global + ll, true, false},
		{"its link-local address still in duplicate address detection", lo + eth0, loAddr + global + llTent, false, false},
		{"the link has carrier but no link-local address yet (addrconf hasn't run)", lo + eth0, loAddr + global, false, false},
		{"another address still in duplicate address detection", lo + eth0, loAddr + ll + globalTent, false, false},
		{"duplicate address detection failed", lo + eth0, loAddr + global + llFailed, false, true},
		{"a link without carrier needs no neighbours yet", lo + eth0 + noCarr, loAddr + ll, true, false},
		{"nor a link that is down", lo + eth0 + down, loAddr + ll, true, false},
		{"nor a tunnel without neighbour discovery", lo + eth0 + tun, loAddr + ll, true, false},
		{"loopback alone", lo, loAddr, true, false},
	} {
		settled, fails := settled(c.links, c.addrs)
		if settled != c.settled || fails != c.fails {
			t.Errorf("%s: settled %v, failed %v; want %v, %v", c.why, settled, fails, c.settled, c.fails)
		}
	}
}
