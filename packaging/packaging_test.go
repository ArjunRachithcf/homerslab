package packaging_test

import (
	"bufio"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/idp/authentik"
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

// The sign-in is one unit running the standard compose command on the package's own compose file (ADR 0014), its
// images pinned by digest at the version the adapter is built for. systemd owns the containers: no restart policy in
// the file, the unit restarts. Its secrets come only from the agent's drop-in, so the unit doesn't start without it.
// The agent may write that drop-in and nothing else of it (agent.RootWrites).
func TestTheSignInIsOneUnitRunningTheStandardComposeCommand(t *testing.T) {
	testenv.Isolate(t)
	u := unit(t, "homerslab-authentik.service")
	const file = "--file /usr/share/homerslab/sign-in/authentik.compose.yaml"
	if s := one(t, u, "ExecStart"); s != "/usr/bin/docker compose --project-name homerslab-authentik "+file+" up --no-log-prefix --quiet-pull" {
		t.Errorf("ExecStart=%s", s)
	}
	if s := one(t, u, "RuntimeDirectory"); s != "homerslab-authentik" || one(t, u, "RuntimeDirectoryMode") != "0700" {
		t.Errorf("the secrets' folder: %s %s", s, u["RuntimeDirectoryMode"])
	}
	if one(t, u, "Restart") != "on-failure" {
		t.Errorf("Restart=%v", u["Restart"])
	}
	for _, k := range []string{"LoadCredentialEncrypted", "Environment", "PrivateMounts", "ProtectSystem"} {
		if len(u[k]) != 0 {
			t.Errorf("%s=%v in the packaged unit: secrets come from the agent's drop-in, and Docker must see them (no mount sandboxing)", k, u[k])
		}
	}
	if c := section(t, "homerslab-authentik.service", "[Unit]")["ConditionPathExists"]; len(c) != 1 || c[0] != "/etc/systemd/system/homerslab-authentik.service.d/50-homerslab-credentials.conf" {
		t.Errorf("ConditionPathExists=%v, want the agent's drop-in", c)
	}
	b, err := os.ReadFile("sign-in/authentik.compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	compose := string(b)
	for _, want := range []string{
		"image: ghcr.io/goauthentik/server@sha256:602227a86455d6772e3d16b7d852713c4f864f40df5df894abd1285bb53979a5",
		"image: docker.io/library/postgres@sha256:aa6cb33cf4c19c765cefcf48d96176258800870a9a1e930ad126200f9d825139",
		"# ghcr.io/goauthentik/server:" + authentik.Version,
		"ipv4_address: " + strings.TrimSuffix(agent.SignInUpstream, ":9000"),
		"name: homerslab-authentik",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("the compose file lacks %q", want)
		}
	}
	for _, bad := range []string{"ports:", "restart: unless-stopped", "restart: always", "docker.sock", "privileged", "AUTHENTIK_BOOTSTRAP_TOKEN:", "env_file"} {
		if strings.Contains(compose, bad) {
			t.Errorf("the compose file has %q", bad)
		}
	}
	if strings.Count(compose, `restart: "no"`) != 2 { // the anchor, and PostgreSQL
		t.Errorf("not every service has its engine restart policy off")
	}
	tmp, _ := os.ReadFile("tmpfiles.d/homerslab.conf")
	if !strings.Contains(string(tmp), "d /etc/systemd/system/homerslab-authentik.service.d 0755 root root -") {
		t.Error("tmpfiles.d doesn't make the folder for the sign-in's drop-in")
	}
	if rw := strings.Join(unit(t, "homerslab-agent.service")["ReadWritePaths"], " "); !strings.Contains(rw, "/etc/systemd/system/homerslab-authentik.service.d") {
		t.Errorf("the agent can't write the sign-in's drop-in: ReadWritePaths=%s", rw)
	}
	if !slices.Contains(agent.RootWrites, "etc/systemd/system/homerslab-authentik.service.d") {
		t.Error("agent.RootWrites lacks the sign-in's drop-in folder")
	}
}
