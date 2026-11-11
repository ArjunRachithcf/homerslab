package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/ArjunRachithcf/homerslab/internal/host"
)

// engineBridges are the container engines' bridges, by their exact names, as Docker and Podman list them. An engine
// that doesn't answer (not installed, not started yet at boot) adds none: no bridge is trusted on its name alone.
func engineBridges(ctx context.Context, run host.Runner) []string {
	var out []string
	if ids, err := run(ctx, host.Cmd{Name: "docker", Args: []string{"network", "ls", "--no-trunc", "--filter", "driver=bridge", "--format", "{{.ID}}"}}); err == nil {
		if list := strings.Fields(string(ids)); len(list) > 0 {
			args := append([]string{"network", "inspect", "--format", `{{.Id}} {{index .Options "com.docker.network.bridge.name"}}`}, list...)
			if b, err := run(ctx, host.Cmd{Name: "docker", Args: args}); err == nil {
				for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
					f := strings.Fields(line)
					switch {
					case len(f) >= 2 && f[1] != "<no" && f[1] != "":
						out = append(out, f[1])
					case len(f) >= 1 && len(f[0]) >= 12:
						out = append(out, "br-"+f[0][:12]) // Docker's name for a bridge it wasn't told to name
					}
				}
			}
		}
	}
	if b, err := run(ctx, host.Cmd{Name: "podman", Args: []string{"network", "ls", "--format", "json"}}); err == nil {
		var nets []struct {
			Driver    string `json:"driver"`
			Interface string `json:"network_interface"`
		}
		if json.Unmarshal(b, &nets) == nil {
			for _, n := range nets {
				if n.Driver == "bridge" && n.Interface != "" {
					out = append(out, n.Interface)
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
