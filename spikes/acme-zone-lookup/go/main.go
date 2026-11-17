// Claim (#79): certmagic's DNS-01 solver, with no resolvers set, looks the zone up through the system resolver
// (/etc/resolv.conf). Here the system resolver is a stub that answers like a mesh's local zone: A records for
// *.example.test, and nothing for SOA.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/libdns"
	"github.com/mholt/acmez/v3/acme"
	"github.com/miekg/dns"
)

type rec struct {
	mu    sync.Mutex
	zones []string
}

func (r *rec) AppendRecords(_ context.Context, zone string, rs []libdns.Record) ([]libdns.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.zones = append(r.zones, zone)
	return rs, nil
}
func (r *rec) DeleteRecords(_ context.Context, zone string, rs []libdns.Record) ([]libdns.Record, error) {
	return rs, nil
}

func main() {
	var mu sync.Mutex
	var asked []string
	dns.HandleFunc(".", func(w dns.ResponseWriter, q *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(q)
		m.Authoritative = true
		mu.Lock()
		asked = append(asked, dns.TypeToString[q.Question[0].Qtype]+" "+q.Question[0].Name)
		mu.Unlock()
		if q.Question[0].Qtype == dns.TypeA {
			rr, _ := dns.NewRR(q.Question[0].Name + " 60 IN A 100.88.7.186")
			m.Answer = append(m.Answer, rr)
		}
		_ = w.WriteMsg(m)
	})
	pc, err := net.ListenPacket("udp", "127.0.0.1:53")
	if err != nil {
		fmt.Println("listen:", err)
		os.Exit(1)
	}
	go (&dns.Server{PacketConn: pc}).ActivateAndServe()
	time.Sleep(200 * time.Millisecond)
	p := &rec{}
	s := &certmagic.DNS01Solver{DNSManager: certmagic.DNSManager{DNSProvider: p, PropagationTimeout: -1}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = s.Present(ctx, acme.Challenge{Identifier: acme.Identifier{Type: "dns", Value: "example.test"}, Token: "t", KeyAuthorization: "t.k"})
	mu.Lock()
	defer mu.Unlock()
	// Only the nameserver lines: the rest of the container's resolv.conf comes from the machine that ran it.
	var servers []string
	for _, l := range strings.Split(must(os.ReadFile("/etc/resolv.conf")), "\n") {
		if strings.HasPrefix(l, "nameserver ") {
			servers = append(servers, l)
		}
	}
	fmt.Printf("the machine's resolver (/etc/resolv.conf): %s\n", strings.Join(servers, "; "))
	fmt.Printf("Present: err=%v\nzones handed to the provider: %v\nqueries the system resolver saw: %v\n", err, p.zones, asked)
}

func must(b []byte, _ error) string { return string(b) }
