package packaging_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// privilegedPaths are where Home must never reach (story 69, ADR 0013): the manager's control socket and runtime
// folder, its database, Admin's socket, the agent's socket and its sealed credentials, the edge's admin API and its
// storage.
var privilegedPaths = []string{"/run/homerslab", "/var/lib/homerslab", "/run/homerslab-admin.sock", "/run/homerslab-agent",
	"/var/lib/homerslab-agent", "/run/homerslab-edge", "/var/lib/homerslab-edge", "/etc/homerslab"}

// Home runs as a process of its own (#21): its own user, in no group but its own, with no capability, the privileged
// paths gone from its view, and the network only to the sign-in's server, for its OIDC client. It reads only the
// projection, which the manager writes into a folder of Home's group.
func TestHomeRunsUnprivilegedAndReachesNothingPrivileged(t *testing.T) {
	testenv.Isolate(t)
	home := unit(t, "homerslab-home.service")
	for k, want := range map[string]string{"ExecStart": "/usr/bin/homerslab home", "User": "homerslab-home", "Group": "homerslab-home",
		"Sockets": "homerslab-home.socket", "CapabilityBoundingSet": "", "AmbientCapabilities": "", "NoNewPrivileges": "yes",
		"ProtectSystem": "strict", "ProtectHome": "yes", "PrivateTmp": "yes", "PrivateDevices": "yes", "PrivateIPC": "yes",
		"ProtectProc": "invisible", "ProcSubset": "pid", "ProtectKernelTunables": "yes", "ProtectKernelModules": "yes",
		"ProtectKernelLogs": "yes", "ProtectControlGroups": "yes", "ProtectClock": "yes", "ProtectHostname": "yes",
		"RestrictSUIDSGID": "yes", "RestrictRealtime": "yes", "RestrictNamespaces": "yes", "LockPersonality": "yes",
		"MemoryDenyWriteExecute": "yes", "RestrictAddressFamilies": "AF_UNIX AF_INET AF_INET6", "SystemCallArchitectures": "native",
		"SystemCallFilter": "@system-service", "SystemCallErrorNumber": "EPERM", "UMask": "0077", "IPAddressDeny": "any",
		"IPAddressAllow":          strings.Split(agent.SignInUpstream, ":")[0],
		"LoadCredentialEncrypted": "manager-home-client-secret:/var/lib/homerslab-agent/credentials/manager-home-client-secret.cred"} {
		if got := home[k]; len(got) != 1 || got[0] != want {
			t.Errorf("Home's %s = %v, want %s", k, got, want)
		}
	}
	if g := home["SupplementaryGroups"]; len(g) != 0 {
		t.Errorf("Home's SupplementaryGroups = %v, want none", g)
	}
	gone := strings.Fields(strings.Join(home["InaccessiblePaths"], " "))
	for _, p := range privilegedPaths {
		if !slices.Contains(gone, "-"+p) {
			t.Errorf("Home's unit leaves %s in its view (InaccessiblePaths = %v)", p, gone)
		}
	}

	// Its socket is for the edge's group only, where the agent tells the edge to find it.
	s := section(t, "homerslab-home.socket", "[Socket]")
	for k, want := range map[string]string{"ListenStream": agent.DefaultEdge.HomeUpstream, "SocketUser": "homerslab-home",
		"SocketGroup": "homerslab-edge", "SocketMode": "0660", "FileDescriptorName": "home", "Service": "homerslab-home.service"} {
		if got := s[k]; len(got) != 1 || got[0] != want {
			t.Errorf("Home's socket's %s = %v, want %s", k, got, want)
		}
	}

	// The projection's folder: the manager writes it, Home's group reads it (setgid, so each file is the group's),
	// nobody else enters it. The manager's unit may write there; nothing else of the manager's is Home's.
	tmpfiles, err := os.ReadFile("tmpfiles.d/homerslab.conf")
	if err != nil || !strings.Contains(string(tmpfiles), "\nd /run/homerslab-projection 2750 homerslab homerslab-home -\n") {
		t.Errorf("tmpfiles.d doesn't make the projection's folder: %v", err)
	}
	if rw := unit(t, "homerslab-manager.service")["ReadWritePaths"]; !slices.Contains(rw, "/run/homerslab-projection") {
		t.Errorf("the manager's ReadWritePaths = %v, want the projection's folder", rw)
	}

	// Home's user is in no group but its own, and nobody else is in its group.
	sysusers, err := os.ReadFile("sysusers.d/homerslab.conf")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(sysusers)) {
		if f := strings.Fields(line); len(f) >= 3 && f[0] == "m" && (f[1] == "homerslab-home" || f[2] == "homerslab-home") {
			t.Errorf("sysusers.d puts %s in %s", f[1], f[2])
		}
	}
	if !strings.Contains(string(sysusers), "\nu homerslab-home 60603:homerslab-home \"HomersLab Home\" / /usr/sbin/nologin\n") {
		t.Errorf("sysusers.d doesn't make Home's own user:\n%s", sysusers)
	}
}
