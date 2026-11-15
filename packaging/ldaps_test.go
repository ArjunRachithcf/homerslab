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
		c[0] != "/etc/systemd/system/homerslab-ldaps.service.d/50-homerslab-certificate.conf" {
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
