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

// Engines that list bridges.
const (
	docker = "docker"
	podman = "podman"
)

// engineBridges are the names the container engines give their bridges, by engine. They are candidates: the agent
// keeps only those the kernel shows are bridges, with names nft can take. An engine missing from the map gave no list
// (it didn't answer, or isn't running), so the agent keeps the list it last confirmed, as far as the kernel still
// shows those bridges; an engine that isn't installed answers with none.
//   - Docker: asked only while docker.service is active, so the question never starts it (it is socket-activated).
//     While it is activating, deactivating or reloading (at every boot and restart), or stopped with its containers
//     running on (live-restore), its last list stands. Between systemd's answer and the question, a stop of
//     docker.service alone can be undone: the question then reaches docker.socket, which starts Docker again. That
//     is socket activation as the admin left it: any client of the socket would do the same, and stopping Docker for
//     good means stopping docker.socket too, after which the question fails (Docker not answering) instead.
//   - Podman: read from its network files (netavark), and its default bridge podman0; no podman command, which
//     would want to write its storage under the agent's read-only sandbox. It always answers.
func engineBridges(ctx context.Context, run host.Runner, root fs.FS) map[string][]string {
	out := map[string][]string{}
	switch unitState(ctx, run, "docker.service") {
	case "active":
		if b, ok := dockerBridges(ctx, run); ok {
			out[docker] = b
		}
	case "not-found": // not installed: none, whatever bridges it left behind
		out[docker] = nil
	}
	var p []string
	if root != nil {
		if _, err := fs.Stat(root, "usr/bin/podman"); err == nil {
			p = append(p, "podman0")
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
				p = append(p, n.Interface)
			}
		}
	}
	out[podman] = p
	return out
}

// unitState is a unit's ActiveState as systemd describes it (active, inactive, failed, activating, deactivating,
// reloading), "not-found" for a unit systemd doesn't have, or "" when systemd didn't say (an error, a timeout).
func unitState(ctx context.Context, run host.Runner, unit string) string {
	out, err := run(ctx, host.Cmd{Name: "systemctl", Args: []string{"show", "-p", "ActiveState", "-p", "LoadState", unit}})
	if err != nil {
		return ""
	}
	props := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			props[k] = v
		}
	}
	if props["LoadState"] == "not-found" {
		return "not-found"
	}
	return props["ActiveState"]
}

// dockerBridges are Docker's bridges, and whether it answered.
func dockerBridges(ctx context.Context, run host.Runner) ([]string, bool) {
	ids, err := run(ctx, host.Cmd{Name: "docker", Args: []string{"network", "ls", "--no-trunc", "--filter", "driver=bridge", "--format", "{{.ID}}"}})
	if err != nil {
		return nil, false
	}
	list := strings.Fields(string(ids))
	if len(list) == 0 {
		return nil, true
	}
	args := append([]string{"network", "inspect", "--format", `{{.Id}} {{index .Options "com.docker.network.bridge.name"}}`}, list...)
	b, err := run(ctx, host.Cmd{Name: "docker", Args: args})
	if err != nil {
		return nil, false
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
	return out, true
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
