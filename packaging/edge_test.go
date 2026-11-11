package packaging_test

import (
	"encoding/json"
	"os"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/edge"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The edge runs unprivileged, with only the capability to bind low ports, configured only through its admin API on a
// socket in a directory only its user and the agent may enter (#78).
func TestTheEdgeRunsUnprivilegedWithOnlyTheCapabilityToBindLowPorts(t *testing.T) {
	testenv.Isolate(t)
	e := unit(t, "homerslab-edge.service")
	for k, want := range map[string]string{
		"User": "homerslab-edge", "Group": "homerslab-edge",
		"ExecStart":             "/usr/bin/caddy run --config /usr/share/homerslab/edge-boot.json",
		"AmbientCapabilities":   "CAP_NET_BIND_SERVICE",
		"CapabilityBoundingSet": "CAP_NET_BIND_SERVICE",
		"NoNewPrivileges":       "yes", "ProtectSystem": "strict", "ProtectHome": "yes", "PrivateTmp": "yes", "PrivateDevices": "yes",
		"RuntimeDirectory": path.Base(path.Dir(agent.DefaultEdge.AdminSocket)), "RuntimeDirectoryMode": "0750",
		"StateDirectory": path.Base(agent.DefaultEdge.Storage), "StateDirectoryMode": "0700", "UMask": "0077",
		"MemoryDenyWriteExecute": "yes", "SystemCallFilter": "@system-service",
	} {
		if got := e[k]; len(got) != 1 || got[0] != want {
			t.Errorf("the edge's %s = %v, want %s", k, got, want)
		}
	}
	if strings.Contains(e["ExecStart"][0], "--resume") {
		t.Error("the edge resumes an autosaved configuration")
	}
	// The agent alone is in the edge's group, which may open its admin socket.
	if got := unit(t, "homerslab-agent.service")["SupplementaryGroups"]; len(got) != 1 || got[0] != "homerslab-edge" {
		t.Errorf("the agent's SupplementaryGroups = %v, want homerslab-edge", got)
	}
	if got := unit(t, "homerslab-manager.service")["SupplementaryGroups"]; len(got) != 0 {
		t.Errorf("the manager is in %v", got)
	}
	sysusers, _ := os.ReadFile("sysusers.d/homerslab.conf")
	if !strings.Contains(string(sysusers), "\nu homerslab-edge ") {
		t.Errorf("sysusers.d doesn't make the edge's user:\n%s", sysusers)
	}
}

// The edge boots with nothing but its admin socket, autosave off: the agent pushes everything else.
func TestTheEdgeBootsWithItsAdminSocketOnlyAndAutosaveOff(t *testing.T) {
	testenv.Isolate(t)
	b, err := os.ReadFile("edge/boot.json")
	if err != nil {
		t.Fatal(err)
	}
	var boot map[string]any
	if err := json.Unmarshal(b, &boot); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"admin": map[string]any{"listen": edge.AdminListen(agent.DefaultEdge.AdminSocket), "config": map[string]any{"persist": false}}}
	wb, _ := json.Marshal(want)
	if !edge.Same(b, wb) {
		t.Errorf("edge/boot.json = %s, want %s", b, wb)
	}
	if !edge.Same(b, []byte(agenttest.Boot)) {
		t.Errorf("agenttest's stand-in boots with %s, not edge/boot.json", agenttest.Boot)
	}
}

// Admin's socket is for the edge's group only, where the agent tells the edge to find it.
func TestAdminsSocketIsForTheEdgeOnly(t *testing.T) {
	testenv.Isolate(t)
	s := section(t, "homerslab-admin.socket", "[Socket]")
	for k, want := range map[string]string{"ListenStream": agent.DefaultEdge.AdminUpstream, "SocketUser": "homerslab",
		"SocketGroup": "homerslab-edge", "SocketMode": "0660", "FileDescriptorName": "admin", "Service": "homerslab-manager.service"} {
		if got := s[k]; len(got) != 1 || got[0] != want {
			t.Errorf("Admin's socket's %s = %v, want %s", k, got, want)
		}
	}
	if got := unit(t, "homerslab-manager.service")["Sockets"]; len(got) != 1 || got[0] != "homerslab-admin.socket" {
		t.Errorf("the manager's Sockets = %v", got)
	}
}

// Every folder the agent writes in under the machine's root is writable in its unit (ProtectSystem=strict leaves the
// rest read-only) and made by tmpfiles.d before it starts (#16's review).
func TestTheAgentsUnitLetsItWriteWhereItWrites(t *testing.T) {
	testenv.Isolate(t)
	rw := strings.Fields(strings.Join(unit(t, "homerslab-agent.service")["ReadWritePaths"], " "))
	tmpfiles, err := os.ReadFile("tmpfiles.d/homerslab.conf")
	if err != nil {
		t.Fatal(err)
	}
	if len(agent.RootWrites) == 0 {
		t.Fatal("the agent lists nowhere it writes")
	}
	for _, dir := range agent.RootWrites {
		if !slices.Contains(rw, "/"+dir) {
			t.Errorf("the agent writes in /%s, which its unit's ReadWritePaths %v leave read-only", dir, rw)
		}
		if !strings.Contains(string(tmpfiles), "\nd /"+dir+" 0755 root root -\n") {
			t.Errorf("tmpfiles.d doesn't make /%s", dir)
		}
	}
	for _, p := range rw {
		if !slices.Contains(agent.RootWrites, strings.TrimPrefix(p, "/")) {
			t.Errorf("the agent's unit lets it write in %s, where it writes nothing", p)
		}
	}
}
