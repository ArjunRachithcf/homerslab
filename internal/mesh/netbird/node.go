package netbird

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/ArjunRachithcf/homerslab/internal/host"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// The node side: NetBird's client on a HomersLab node, as the agent runs it.
const (
	// Interface is the client's WireGuard interface, the node's mesh interface in the firewall table.
	Interface = "wt0"
	// WireGuardPort is the client's UDP port, which the firewall table lets in so peers connect directly.
	WireGuardPort = 51820
	// DropInPath is where the agent puts the client service's environment.
	DropInPath = "etc/systemd/system/netbird.service.d/10-homerslab.conf"
)

// ServiceEnv is the environment of NetBird's daemon on every node. NB_DISABLE_NAT_MAPPER: since v0.68 the client asks
// the router for a port through UPnP, NAT-PMP or PCP, and this is the only switch (the API has none); HomersLab never
// opens a router port (ADR 0005).
var ServiceEnv = []string{"NB_DISABLE_NAT_MAPPER=true"}

// DropIn is the systemd drop-in that gives NetBird's service ServiceEnv.
func DropIn() []byte {
	var b strings.Builder
	b.WriteString("# Written by HomersLab's agent, which puts it back if changed. NetBird's client never asks the router\n")
	b.WriteString("# for a port (UPnP, NAT-PMP, PCP): HomersLab opens no router port, ever.\n[Service]\n")
	for _, e := range ServiceEnv {
		b.WriteString("Environment=" + e + "\n")
	}
	return []byte(b.String())
}

// Node is NetBird's client on this machine.
type Node struct{ Run host.Runner }

// Up joins the mesh at managementURL with a setup key, as hostname. The key goes in the environment, never on the
// command line. The caller has checked the URL (mesh.CheckURL): the agent does, before it calls Up.
func (n Node) Up(ctx context.Context, managementURL, hostname string, key mesh.Secret) error {
	if key == "" {
		return errors.New("no setup key")
	}
	_, err := n.Run(ctx, host.Cmd{
		Name: "netbird",
		Args: []string{"up", "--management-url", managementURL, "--hostname", hostname,
			"--wireguard-port", strconv.Itoa(WireGuardPort), "--no-browser"},
		Env: []string{"NB_SETUP_KEY=" + key.Reveal()},
	})
	return err
}

// Status is the client's view of the mesh.
type Status struct {
	Connected bool
	IP        netip.Addr
	FQDN      string
}

// Status asks the client how it is.
func (n Node) Status(ctx context.Context) (Status, error) {
	out, err := n.Run(ctx, host.Cmd{Name: "netbird", Args: []string{"status", "--json"}})
	if err != nil {
		return Status{}, err
	}
	var s struct {
		Management struct {
			Connected bool `json:"connected"`
		} `json:"management"`
		IP   string `json:"netbirdIp"`
		FQDN string `json:"fqdn"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return Status{}, fmt.Errorf("netbird status: %w", err)
	}
	st := Status{Connected: s.Management.Connected, FQDN: s.FQDN}
	if p, err := netip.ParsePrefix(s.IP); err == nil {
		st.IP = p.Addr()
	} else if a, err := netip.ParseAddr(s.IP); err == nil {
		st.IP = a
	}
	return st, nil
}
