package packaging_test

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// unit reads a systemd unit's [Service] settings.
func unit(t *testing.T, name string) map[string][]string {
	t.Helper()
	f, err := os.Open("systemd/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string][]string{}
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && section == "[Service]" && !strings.HasPrefix(line, "#") {
			out[k] = append(out[k], v)
		}
	}
	return out
}

func one(t *testing.T, u map[string][]string, k string) string {
	t.Helper()
	if len(u[k]) != 1 {
		t.Fatalf("%s = %v, want one value", k, u[k])
	}
	return u[k][0]
}

// The one binary runs twice on the primary: the manager as the unprivileged homerslab user, the agent as root.
func TestTheManagerRunsUnprivilegedAndTheAgentAsRoot(t *testing.T) {
	testenv.Isolate(t)
	manager, agent := unit(t, "homerslab-manager.service"), unit(t, "homerslab-agent.service")

	if got := one(t, manager, "ExecStart"); got != "/usr/bin/homerslab manager" {
		t.Errorf("manager ExecStart = %q", got)
	}
	if got := one(t, agent, "ExecStart"); got != "/usr/bin/homerslab agent" {
		t.Errorf("agent ExecStart = %q", got)
	}
	if one(t, manager, "User") != "homerslab" || one(t, manager, "Group") != "homerslab" {
		t.Errorf("the manager runs as %v:%v, want homerslab", manager["User"], manager["Group"])
	}
	for _, k := range []string{"NoNewPrivileges", "ProtectSystem", "ProtectHome", "PrivateTmp", "PrivateDevices", "CapabilityBoundingSet", "UMask"} {
		if len(manager[k]) == 0 {
			t.Errorf("the manager's unit doesn't set %s", k)
		}
	}
	if got := one(t, manager, "CapabilityBoundingSet"); got != "" {
		t.Errorf("the manager's capabilities = %q, want none", got)
	}
	if u := agent["User"]; len(u) != 0 && u[0] != "root" {
		t.Errorf("the agent runs as %v, want root", u)
	}
	// The agent only reads facts so far; later tickets loosen this as its contract grows.
	for k, want := range map[string]string{"ProtectHome": "read-only", "ProtectKernelModules": "yes", "ProtectClock": "yes",
		"ProtectKernelLogs": "yes", "NoNewPrivileges": "yes", "ProtectSystem": "strict", "PrivateTmp": "yes",
		"ProtectKernelTunables": "yes", "ProtectControlGroups": "yes", "RestrictSUIDSGID": "yes",
		"CapabilityBoundingSet": "CAP_CHOWN CAP_DAC_READ_SEARCH", "RestrictAddressFamilies": "AF_UNIX",
		"PrivateNetwork": "yes", "SystemCallFilter": "@system-service", "SystemCallErrorNumber": "EPERM",
		"MemoryDenyWriteExecute": "yes", "LockPersonality": "yes"} {
		if got := agent[k]; len(got) != 1 || got[0] != want {
			t.Errorf("the agent's %s = %v, want %s", k, got, want)
		}
	}
	if got := one(t, agent, "UMask"); got != "0077" {
		t.Errorf("the agent's UMask = %q, want 0077", got)
	}

	sysusers, err := os.ReadFile("sysusers.d/homerslab.conf")
	if err != nil || !strings.Contains(string(sysusers), "u homerslab ") {
		t.Errorf("sysusers.d/homerslab.conf should create the homerslab user: %v %q", err, sysusers)
	}
}
