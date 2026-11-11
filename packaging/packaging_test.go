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
	return section(t, name, "[Service]")
}

// section reads one section of a systemd unit.
func section(t *testing.T, name, want string) map[string][]string {
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
		if k, v, ok := strings.Cut(line, "="); ok && section == want && !strings.HasPrefix(line, "#") {
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
	// The agent reads facts, owns the firewall table (CAP_NET_ADMIN, netlink, the host's own network), runs the mesh
	// client's service (a drop-in under /etc/systemd/system), and seals credentials in its state directory; later
	// tickets loosen this as its contract grows.
	for k, want := range map[string]string{"ProtectHome": "read-only", "ProtectKernelModules": "yes", "ProtectClock": "yes",
		"ProtectKernelLogs": "yes", "NoNewPrivileges": "yes", "ProtectSystem": "strict", "PrivateTmp": "yes",
		"ProtectKernelTunables": "yes", "ProtectControlGroups": "yes", "RestrictSUIDSGID": "yes",
		"CapabilityBoundingSet": "CAP_CHOWN CAP_DAC_READ_SEARCH CAP_NET_ADMIN", "RestrictAddressFamilies": "AF_UNIX AF_NETLINK AF_INET AF_INET6",
		"PrivateNetwork": "no", "SystemCallFilter": "@system-service", "SystemCallErrorNumber": "EPERM",
		"MemoryDenyWriteExecute": "yes", "LockPersonality": "yes", "StateDirectory": "homerslab-agent",
		"StateDirectoryMode": "0700", "Type": "notify", "NotifyAccess": "main", "ExecStartPre": "+/usr/bin/systemd-creds setup", "TimeoutStartSec": "30s"} {
		if got := agent[k]; len(got) != 1 || got[0] != want {
			t.Errorf("the agent's %s = %v, want %s", k, got, want)
		}
	}
	if got := one(t, agent, "UMask"); got != "0077" {
		t.Errorf("the agent's UMask = %q, want 0077", got)
	}

	// The agent orders itself against nothing of the engines' or the network's (a socket-activated engine would wait
	// for it, and it for the engine): homerslab-firewall-restore loads the table before them instead.
	agentUnit, _ := os.ReadFile("systemd/homerslab-agent.service")
	for _, want := range []string{"\nWants=homerslab-firewall-restore.service\n", "\nAfter=homerslab-firewall-restore.service\n", "\nBefore=homerslab-manager.service\n"} {
		if !strings.Contains(string(agentUnit), want) {
			t.Errorf("the agent's unit lacks %q", strings.TrimSpace(want))
		}
	}
	for _, never := range []string{"network-pre", "docker", "podman", "netbird"} {
		if strings.Contains(unitSection(string(agentUnit), "[Unit]"), never) {
			t.Errorf("the agent's unit is ordered against %s", never)
		}
	}
	for _, u := range []string{"docker", "podman", "netbird"} {
		b, err := os.ReadFile("systemd/" + u + ".service.d/05-homerslab-firewall.conf")
		if err != nil || !strings.Contains(string(b), "\n[Unit]\nRequires=homerslab-firewall-restore.service\nAfter=homerslab-firewall-restore.service\n") {
			t.Errorf("%s doesn't require the restored table: %v", u, err)
		}
	}
	if b, err := os.ReadFile("tmpfiles.d/homerslab.conf"); err != nil || !strings.Contains(string(b), "d /etc/systemd/system/netbird.service.d 0755 root root -") {
		t.Errorf("tmpfiles.d doesn't make the folder for NetBird's drop-in: %v", err)
	}

	sysusers, err := os.ReadFile("sysusers.d/homerslab.conf")
	if err != nil || !strings.Contains(string(sysusers), "u homerslab ") {
		t.Errorf("sysusers.d/homerslab.conf should create the homerslab user: %v %q", err, sysusers)
	}
}

func unitSection(unit, name string) string {
	_, after, _ := strings.Cut(unit, "\n"+name+"\n")
	section, _, _ := strings.Cut(after, "\n[")
	return section
}

// The firewall table is loaded at boot before the network, by a oneshot the engines require.
func TestTheFirewallTableIsRestoredBeforeTheNetwork(t *testing.T) {
	testenv.Isolate(t)
	b, err := os.ReadFile("systemd/homerslab-firewall-restore.service")
	if err != nil {
		t.Fatal(err)
	}
	u := string(b)
	for _, want := range []string{"DefaultDependencies=no", "Wants=network-pre.target", "Before=network-pre.target shutdown.target",
		"After=local-fs.target nftables.service firewalld.service", "Conflicts=shutdown.target"} {
		if !strings.Contains(unitSection(u, "[Unit]"), "\n"+want+"\n") {
			t.Errorf("[Unit] lacks %q", want)
		}
	}
	svc := unit(t, "homerslab-firewall-restore.service")
	for k, want := range map[string]string{"Type": "oneshot", "RemainAfterExit": "yes", "ExecStart": "/usr/bin/homerslab firewall restore",
		"CapabilityBoundingSet": "CAP_NET_ADMIN", "PrivateNetwork": "no"} {
		if got := one(t, svc, k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(unitSection(u, "[Install]"), "WantedBy=sysinit.target") {
		t.Error("the restore isn't part of the boot")
	}
}
