package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/netip"
	"slices"
	"strings"

	"github.com/ArjunRachithcf/homerslab/internal/host"
)

// engineBridges are the names the container engines give their bridges. They are candidates: the agent keeps only
// those the kernel shows are bridges, with names nft can take.
//   - Docker: asked only while docker.service runs, so the question never starts it (it is socket-activated).
//   - Podman: read from its network files (netavark), and its default bridge podman0; no podman command, which
//     would want to write its storage under the agent's read-only sandbox.
func engineBridges(ctx context.Context, run host.Runner, root fs.FS) []string {
	var out []string
	if _, err := run(ctx, host.Cmd{Name: "systemctl", Args: []string{"is-active", "--quiet", "docker.service"}}); err == nil {
		out = append(out, dockerBridges(ctx, run)...)
	}
	if root != nil {
		if _, err := fs.Stat(root, "usr/bin/podman"); err == nil {
			out = append(out, "podman0")
		}
		files, _ := fs.Glob(root, "etc/containers/networks/*.json")
		for _, f := range files {
			b, err := fs.ReadFile(root, f)
			if err != nil {
				continue
			}
			var n struct {
				Driver    string `json:"driver"`
				Interface string `json:"network_interface"`
			}
			if json.Unmarshal(b, &n) == nil && n.Driver == "bridge" && n.Interface != "" {
				out = append(out, n.Interface)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func dockerBridges(ctx context.Context, run host.Runner) []string {
	ids, err := run(ctx, host.Cmd{Name: "docker", Args: []string{"network", "ls", "--no-trunc", "--filter", "driver=bridge", "--format", "{{.ID}}"}})
	if err != nil {
		return nil
	}
	list := strings.Fields(string(ids))
	if len(list) == 0 {
		return nil
	}
	args := append([]string{"network", "inspect", "--format", `{{.Id}} {{index .Options "com.docker.network.bridge.name"}}`}, list...)
	b, err := run(ctx, host.Cmd{Name: "docker", Args: args})
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		id, name, _ := strings.Cut(line, " ")
		switch {
		case name != "" && name != "<no value>":
			out = append(out, name)
		case len(id) >= 12:
			out = append(out, "br-"+id[:12]) // Docker's name for a bridge it wasn't told to name
		}
	}
	return out
}

// resolvers are the DNS servers this machine uses, as its resolv.conf files say (systemd-resolved's upstream list
// included), which an engine may hand its containers.
func resolvers(root fs.FS) []netip.Addr {
	if root == nil {
		return nil
	}
	var out []netip.Addr
	for _, f := range []string{"etc/resolv.conf", "run/systemd/resolve/resolv.conf"} {
		b, err := fs.ReadFile(root, f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) >= 2 && fields[0] == "nameserver" {
				if a, err := netip.ParseAddr(fields[1]); err == nil && !a.IsLoopback() && !a.IsUnspecified() {
					out = append(out, a.WithZone(""))
				}
			}
		}
	}
	slices.SortFunc(out, func(x, y netip.Addr) int { return x.Compare(y) })
	return slices.Compact(out)
}
