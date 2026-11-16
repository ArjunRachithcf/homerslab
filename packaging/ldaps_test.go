package packaging_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/idp/authentik"
	"github.com/ArjunRachithcf/homerslab/internal/ldaps"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The sign-in listener's LDAPS port (#204): systemd binds it at the listener's own address, and the process behind it
// runs as its own user with no capability, talks only to the apps' and the sign-in's networks, and hands connections
// to the packaged LDAP outpost's address.
func TestTheLDAPSPortIsASocketAndAnUnprivilegedProcess(t *testing.T) {
	testenv.Isolate(t)
	s := section(t, "homerslab-ldaps.socket", "[Socket]")
	for k, want := range map[string]string{"ListenStream": fmt.Sprintf("%s:%d", agent.SignInListen, ldaps.Port), "FreeBind": "yes",
		"Service": "homerslab-ldaps.service", "IPAddressDeny": "any", "IPAddressAllow": "172.31.240.0/20"} {
		if got := s[k]; len(got) != 1 || got[0] != want {
			t.Errorf("the socket's %s = %v, want %s", k, got, want)
		}
	}
	u := unit(t, "homerslab-ldaps.service")
	for k, want := range map[string]string{"User": "homerslab-ldaps", "Group": "homerslab-ldaps", "CapabilityBoundingSet": "",
		"AmbientCapabilities": "", "NoNewPrivileges": "yes", "ProtectSystem": "strict", "ProtectHome": "yes", "PrivateDevices": "yes",
		"RestrictAddressFamilies": "AF_UNIX AF_INET", "IPAddressDeny": "any", "IPAddressAllow": "172.31.240.0/20",
		"SystemCallFilter": "@system-service", "MemoryDenyWriteExecute": "yes"} {
		if got := u[k]; len(got) != 1 || got[0] != want {
			t.Errorf("the service's %s = %v, want %s", k, got, want)
		}
	}
	for _, k := range []string{"LoadCredentialEncrypted", "LoadCredential", "Environment", "SetCredential"} {
		if len(u[k]) != 0 {
			t.Errorf("%s=%v in the packaged unit: the certificate and the name come from the agent's drop-in", k, u[k])
		}
	}
	if c := section(t, "homerslab-ldaps.service", "[Unit]")["ConditionPathExists"]; len(c) != 1 ||
		c[0] != "/etc/systemd/system/homerslab-ldaps.service.d/50-homerslab-certificates.conf" {
		t.Errorf("ConditionPathExists=%v, want the agent's drop-in", c)
	}
	exec := one(t, u, "ExecStart")
	compose, err := os.ReadFile("sign-in/authentik.compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, outpost, _ := strings.Cut(string(compose), "\n  ldap:\n")
	addr := ""
	if _, rest, ok := strings.Cut(outpost, "ipv4_address: "); ok {
		addr, _, _ = strings.Cut(rest, "\n")
	}
	if addr != agent.LDAPOutpost.String() {
		t.Errorf("the outpost is at %s; the agent's firewall keeps %s", addr, agent.LDAPOutpost)
	}
	if want := "/usr/bin/homerslab edge ldaps --name ${HOMERSLAB_SIGN_IN} --cert %d/tls.crt --key %d/tls.key --upstream " + addr + ":3389"; addr == "" || exec != want {
		t.Errorf("ExecStart=%s\nwant %s", exec, want)
	}
	for _, want := range []string{"profiles: [ldap]", "image: ghcr.io/goauthentik/ldap@sha256:", "# ghcr.io/goauthentik/ldap:" + authentik.Version + "\n", "- ldap-token"} {
		if !strings.Contains(outpost, want) {
			t.Errorf("the LDAP outpost's service lacks %q", want)
		}
	}
	for _, bad := range []string{"ports:", "AUTHENTIK_TOKEN:", "env_file", "6636"} {
		if strings.Contains(outpost, bad) {
			t.Errorf("the LDAP outpost's service has %q", bad)
		}
	}
}

// The LDAP outpost is its own unit (#204's review): the sign-in's project and file, its one service, so its exit ends
// its own command and systemd starts it again, while the sign-in's command never watches it and Authentik stays up.
// Its token comes only from the agent's drop-in, into a runtime directory of its own.
func TestTheLDAPOutpostIsAUnitOfItsOwn(t *testing.T) {
	testenv.Isolate(t)
	u := unit(t, "homerslab-ldap.service")
	const compose = "/usr/bin/docker compose --project-name homerslab-authentik --file /usr/share/homerslab/sign-in/authentik.compose.yaml --profile ldap "
	if s := one(t, u, "ExecStart"); s != compose+"up --no-deps --no-log-prefix --quiet-pull --abort-on-container-exit ldap" {
		t.Errorf("ExecStart=%s", s)
	}
	if s := one(t, u, "ExecStopPost"); s != compose+"stop ldap" {
		t.Errorf("ExecStopPost=%s, want the outpost stopped alone", s)
	}
	for k, want := range map[string]string{"Restart": "always", "SuccessExitStatus": "130", "RuntimeDirectory": "homerslab-ldap", "RuntimeDirectoryMode": "0700"} {
		if got := one(t, u, k); got != want {
			t.Errorf("%s=%s, want %s", k, got, want)
		}
	}
	if c := section(t, "homerslab-ldap.service", "[Unit]")["ConditionPathExists"]; len(c) != 1 ||
		c[0] != "/etc/systemd/system/homerslab-ldap.service.d/50-homerslab-credentials.conf" {
		t.Errorf("ConditionPathExists=%v, want the agent's drop-in", c)
	}
	for _, k := range []string{"LoadCredentialEncrypted", "Environment", "PartOf", "BindsTo"} {
		if len(u[k]) != 0 {
			t.Errorf("%s=%v in the packaged unit", k, u[k])
		}
	}
	// The sign-in's own command starts and watches the sign-in's services, never the outpost's profile.
	if s := one(t, unit(t, "homerslab-authentik.service"), "ExecStart"); strings.Contains(s, "--profile") || strings.Contains(s, "ldap") {
		t.Errorf("the sign-in's ExecStart=%s: it would watch the outpost", s)
	}
	b, err := os.ReadFile("sign-in/authentik.compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "  ldap-token:\n    file: /run/homerslab-ldap/ldap-token\n") {
		t.Error("the outpost's token isn't read from its own unit's runtime directory")
	}
}
