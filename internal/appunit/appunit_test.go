package appunit_test

import (
	"bytes"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/appunit"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

var update = flag.Bool("update", false, "write the golden renders anew")

// bentoPDF is BentoPDF as the agent installs it on a machine with SELinux: one service, no secret.
func bentoPDF() appunit.App {
	return appunit.App{ID: "bentopdf", Product: "BentoPDF", Plan: "p-8c125cd2f9f38dfdd95c9c0ba3cd2663",
		Project: engine.Project{Name: "homerslab-bentopdf", Services: []engine.Service{{Name: "web", Image: "ghcr.io/alam00000/bentopdf-simple:2.8.10@sha256:b5227a308e796d2bd00d2eb6da9d7296093107a8be7efef38a006d06506dc3c0"}}},
		Service: "web", Upstream: netip.MustParseAddrPort("172.31.240.2:8080"), Subnet: netip.MustParsePrefix("172.31.240.0/28"),
		Level: "s0:c600,c601", SELinux: true, Dir: "/var/lib/homerslab-agent/apps/bentopdf"}
}

// withSecrets is an app of several services with a secret two of them read, and networks of its own.
func withSecrets() appunit.App {
	return appunit.App{ID: "everything", Product: "Everything", Plan: "p-0123456789abcdef0123456789abcdef",
		Project: engine.Project{Name: "homerslab-everything", Secrets: []string{"db-password", "secret-key"}, Services: []engine.Service{
			{Name: "web", Networks: []string{"back", "front"}}, {Name: "db", Networks: []string{"back"}}, {Name: "worker"}}},
		Service: "web", Upstream: netip.MustParseAddrPort("172.31.240.18:8080"), Subnet: netip.MustParsePrefix("172.31.240.16/28"),
		Level: "s0:c602,c603", SELinux: true, Dir: "/var/lib/homerslab-agent/apps/everything",
		Secrets: []appunit.Secret{{Name: "secret-key", UID: 1000}, {Name: "db-password", UID: 999}}}
}

// Golden renders (spec: Testing Decisions): the unit and the compose file the agent writes for an app, byte for byte.
// A deliberate change runs `go test ./internal/appunit -update` and explains the diff.
func TestAnAppsUnitAndComposeFileAreTheirGoldenRenders(t *testing.T) {
	testenv.Isolate(t)
	noSELinux := withSecrets()
	noSELinux.SELinux = false
	for name, a := range map[string]appunit.App{"bentopdf": bentoPDF(), "with-secrets": withSecrets(), "without-selinux": noSELinux} {
		for file, got := range map[string][]byte{name + ".service": appunit.UnitFile(a), name + ".compose.yaml": appunit.Override(a)} {
			t.Run(file, func(t *testing.T) {
				path := filepath.Join("testdata", file)
				if *update {
					if err := os.WriteFile(path, got, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (run with -update to write it)", err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("the render differs from %s:\n%s", path, got)
				}
			})
		}
	}
}

// systemd owns the app (#19): every service's restart policy is off, so a container never outlives its unit or comes
// back without it; the unit restarts the app instead.
func TestEveryServiceRunsWithoutARestartPolicyAndTheUnitRestarts(t *testing.T) {
	testenv.Isolate(t)
	a := withSecrets()
	o := string(appunit.Override(a))
	for _, s := range a.Project.Services {
		block := o[strings.Index(o, "  \""+s.Name+"\":\n"):]
		if !strings.HasPrefix(block, "  \""+s.Name+"\":\n    restart: \"no\"\n    security_opt:\n      - \"label=level:s0:c602,c603\"\n") {
			t.Errorf("%s isn't left without a restart policy at the app's level:\n%s", s.Name, o)
		}
	}
	u := string(appunit.UnitFile(a))
	for _, want := range []string{"\nRestart=always\n", "\nExecStart=/usr/bin/docker compose --project-name homerslab-everything ",
		" up --no-log-prefix --quiet-pull --abort-on-container-exit\n", "\nWantedBy=multi-user.target\n"} {
		if !strings.Contains(u, want) {
			t.Errorf("the unit lacks %q:\n%s", want, u)
		}
	}
}

// Secrets (ADR 0011): each is loaded from the credential the agent sealed under the name <app>.<secret>, copied for the
// one container user that reads it (0400) into the unit's own runtime directory, labelled at the app's level, and handed
// in only as a Compose secret: never an environment variable, never a shared folder.
func TestSecretsReachTheContainersOnlyAsFilesAtTheAppsLevel(t *testing.T) {
	testenv.Isolate(t)
	a := withSecrets()
	u, o := string(appunit.UnitFile(a)), string(appunit.Override(a))
	for _, want := range []string{
		"LoadCredentialEncrypted=everything.db-password:/var/lib/homerslab-agent/apps/everything/credentials/db-password.cred\n",
		"ExecStartPre=/usr/bin/install -m 0400 -o 999 -g 999 %d/everything.db-password /run/homerslab-app-everything/db-password\n",
		"ExecStartPre=/usr/bin/install -m 0400 -o 1000 -g 1000 %d/everything.secret-key /run/homerslab-app-everything/secret-key\n",
		"ExecStartPre=/usr/bin/chcon -t container_file_t -l s0:c602,c603 /run/homerslab-app-everything/db-password /run/homerslab-app-everything/secret-key\n",
		"RuntimeDirectory=homerslab-app-everything\nRuntimeDirectoryMode=0700\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("the unit lacks %q:\n%s", want, u)
		}
	}
	if !strings.Contains(o, "secrets:\n  \"db-password\":\n    file: \"/run/homerslab-app-everything/db-password\"\n  \"secret-key\":\n    file: \"/run/homerslab-app-everything/secret-key\"\n") {
		t.Errorf("the secrets aren't the unit's copies:\n%s", o)
	}
	for _, bad := range []string{"environment", "Environment=", "volumes", ":z", ":Z"} {
		if strings.Contains(u, bad) || strings.Contains(o, bad) {
			t.Errorf("the app's files carry %q", bad)
		}
	}
	noSELinux := a
	noSELinux.SELinux = false
	if strings.Contains(string(appunit.UnitFile(noSELinux)), "chcon") {
		t.Error("a machine without SELinux is asked to label")
	}
}

// The app's one upstream is on a network of its own, at a fixed address; the service keeps the networks it had.
func TestTheUpstreamIsAFixedAddressOnANetworkOfItsOwn(t *testing.T) {
	testenv.Isolate(t)
	b, s := string(appunit.Override(bentoPDF())), string(appunit.Override(withSecrets()))
	if !strings.Contains(b, "    networks:\n      \"default\": {}\n      \"homerslab-upstream\":\n        ipv4_address: \"172.31.240.2\"\n") {
		t.Errorf("a service on the default network loses it, or has no upstream:\n%s", b)
	}
	if strings.Contains(s, "\"default\": {}") || !strings.Contains(s, "      \"homerslab-upstream\":\n        ipv4_address: \"172.31.240.18\"\n") {
		t.Errorf("a service with networks of its own:\n%s", s)
	}
	if !strings.Contains(s, "        - subnet: \"172.31.240.16/28\"\n          gateway: \"172.31.240.17\"\n") {
		t.Errorf("the upstream's network:\n%s", s)
	}
	for _, bad := range []string{"ports", "network_mode", "host"} {
		if strings.Contains(b, bad) {
			t.Errorf("the agent's compose file has %q", bad)
		}
	}
}
