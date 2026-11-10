// gocheck drives the spike's NetBird with the official Go REST client
// (github.com/netbirdio/netbird/shared/management/client/rest, v0.82.0): it reads what the
// curl run created and does one create/delete round trip, so request and response types are
// both exercised against the real server.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/netbirdio/netbird/shared/management/client/rest"
	"github.com/netbirdio/netbird/shared/management/http/api"
)

func main() {
	tok, err := os.ReadFile("/secrets/token")
	must("read token", err)
	c := rest.New("http://nb.hlspike.test", strings.TrimSpace(string(tok)))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	st, err := c.Instance.GetStatus(ctx)
	must("instance", err)
	fmt.Printf("instance: setup_required=%v\n", st.SetupRequired)

	peers, err := c.Peers.List(ctx)
	must("peers", err)
	show("peers", peers)

	groups, err := c.Groups.List(ctx)
	must("groups", err)
	show("groups", groups)

	policies, err := c.Policies.List(ctx)
	must("policies", err)
	show("policies", policies)

	keys, err := c.SetupKeys.List(ctx)
	must("setup keys", err)
	show("setup keys", keys)

	zones, err := c.DNSZones.ListZones(ctx)
	must("zones", err)
	show("zones", zones)

	clusters, err := c.ReverseProxyClusters.List(ctx)
	must("clusters", err)
	show("clusters", clusters)

	domains, err := c.ReverseProxyDomains.List(ctx)
	must("domains", err)
	show("domains", domains)

	services, err := c.ReverseProxyServices.List(ctx)
	must("services", err)
	show("services", services)

	tokens, err := c.ReverseProxyTokens.List(ctx)
	must("proxy tokens", err)
	show("proxy tokens", tokens)

	// Round trip: a group, and a record in the first zone.
	g, err := c.Groups.Create(ctx, api.GroupRequest{Name: "go-client-check"})
	must("create group", err)
	fmt.Printf("created group %q (%s)\n", g.Name, g.Id)
	must("delete group", c.Groups.Delete(ctx, g.Id))
	fmt.Println("deleted group")

	if len(zones) > 0 {
		r, err := c.DNSZones.CreateRecord(ctx, zones[0].Id, api.DNSRecordRequest{
			Name: "go." + zones[0].Domain, Type: api.DNSRecordTypeA, Content: "100.88.1.1", Ttl: 60,
		})
		must("create record", err)
		fmt.Printf("created record %s %s %s\n", r.Name, r.Type, r.Content)
		must("delete record", c.DNSZones.DeleteRecord(ctx, zones[0].Id, r.Id))
		fmt.Println("deleted record")
	}

	// A refused request comes back as an error carrying the server's message.
	_, err = c.ReverseProxyServices.Create(ctx, api.ServiceRequest{Name: "x", Domain: "x.example.org", Enabled: true})
	fmt.Printf("refused service create -> %v\n", err)
}

func show(what string, v any) {
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	fmt.Printf("%s: %s\n", what, s)
}

func must(what string, err error) {
	if err != nil {
		fmt.Printf("FAIL %s: %v\n", what, err)
		os.Exit(1)
	}
}
