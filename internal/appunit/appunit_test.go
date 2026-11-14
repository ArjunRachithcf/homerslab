package appunit_test

import (
	"bytes"
	"flag"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"

	"github.com/ArjunRachithcf/homerslab/internal/appunit"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

var update = flag.Bool("update", false, "write the golden renders anew")

// bentoPDF is BentoPDF as the agent installs it on a machine with SELinux: one service, no secret.
func bentoPDF() appunit.App {
	return appunit.App{ID: "bentopdf", Product: "BentoPDF", Plan: "p-8c125cd2f9f38dfdd95c9c0ba3cd2663",
		Project: engine.Project{Name: "homerslab-bentopdf", Services: []engine.Service{{Name: "web", Image: "ghcr.io/alam00000/bentopdf-simple:2.8.10@sha256:b5227a308e796d2bd00d2eb6da9d7296093107a8be7efef38a006d06506dc3c0"}},
			Networks: []engine.Network{{Name: "default"}}},
		Service: "web", Upstream: netip.MustParseAddrPort("172.31.240.2:8080"), Subnet: netip.MustParsePrefix("172.31.240.0/28"),
		Level: "s0:c600,c601", SELinux: true, Dir: "/var/lib/homerslab-agent/apps/bentopdf"}
}

// withSecrets is an app of several services with a secret two of them read, and networks of its own.
func withSecrets() appunit.App {
	return appunit.App{ID: "everything", Product: "Everything", Plan: "p-0123456789abcdef0123456789abcdef",
		Project: engine.Project{Name: "homerslab-everything", Secrets: []string{"db-password", "secret-key"}, Services: []engine.Service{
			{Name: "web", Networks: []string{"back", "front"}}, {Name: "db", Networks: []string{"back"}}, {Name: "worker"}},
			Networks: []engine.Network{{Name: "back"}, {Name: "default"}, {Name: "front"}}},
		Service: "web", Upstream: netip.MustParseAddrPort("172.31.240.18:8080"), Subnet: netip.MustParsePrefix("172.31.240.16/28"),
		Level: "s0:c602,c603", Slot: 1, SELinux: true, Dir: "/var/lib/homerslab-agent/apps/everything",
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
		if !strings.HasPrefix(block, "  \""+s.Name+"\":\n    restart: \"no\"\n    deploy:\n      restart_policy:\n        condition: \"none\"\n"+
			"    security_opt:\n      - \"label=level:s0:c602,c603\"\n") {
			t.Errorf("%s isn't left without a restart policy at the app's level:\n%s", s.Name, o)
		}
	}
	u := string(appunit.UnitFile(a))
	for _, want := range []string{"\nRestart=always\n", "\nStandardOutput=null\nStandardError=null\n", "\nExecStart=/usr/bin/docker compose --project-name homerslab-everything ",
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

// Every bridge of an app's is named by the agent, after the app's slot (#82): the firewall table keeps this machine's
// processes out of an app's networks by those names, before the engine has made them.
func TestEveryNetworkOfAnAppsHasABridgeTheAgentNamed(t *testing.T) {
	testenv.Isolate(t)
	o := string(appunit.Override(withSecrets()))
	for net, br := range map[string]string{"back": "hla1n0", "default": "hla1n1", "front": "hla1n2", "homerslab-upstream": "hla1u"} {
		if !strings.Contains(o, "\n  \""+net+"\":\n") || !strings.Contains(o, "      com.docker.network.bridge.name: \""+br+"\"\n") {
			t.Errorf("%s isn't on bridge %s:\n%s", net, br, o)
		}
	}
	if got := appunit.Bridge(199, 63); len(got) > 15 {
		t.Errorf("a bridge name over Linux's 15 characters: %s", got)
	}
}

// No free text reaches the files the agent writes (#195's review): a name that YAML would read as several lines (U+0085,
// U+2028 and U+2029 break lines in YAML) or that would continue a unit's line changes nothing. The agent's compose file
// is one YAML document with exactly the keys the agent writes, and the unit has only the agent's lines.
func TestNoTextOfTheAppsEndsALineOfItsFiles(t *testing.T) {
	testenv.Isolate(t)
	const probe = "X\u0085---\u0085services: {web: {privileged: true, pid: host, volumes: [\"/:/host\"]}}\u0085---\u0085#"
	for name, a := range map[string]appunit.App{
		"NEL":                    func() appunit.App { a := withSecrets(); a.Product = probe; return a }(),
		"the line separator":     func() appunit.App { a := withSecrets(); a.Product = "X\u2028include: [/etc/shadow]\u2028#"; return a }(),
		"a backslash at its end": func() appunit.App { a := withSecrets(); a.Product = "X\\"; return a }(),
		"a plan that isn't one":  func() appunit.App { a := withSecrets(); a.Plan = "p-1\u2028include: [/x]"; return a }(),
		"a service's odd name": func() appunit.App {
			a := withSecrets()
			a.Project.Services = append(a.Project.Services, engine.Service{Name: "odd\u0085privileged: true"})
			return a
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			o := appunit.Override(a)
			dec := yaml.NewDecoder(bytes.NewReader(o))
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				t.Fatalf("the agent's compose file doesn't parse: %v\n%s", err, o)
			}
			var more any
			if err := dec.Decode(&more); err == nil {
				t.Errorf("a second YAML document:\n%s", o)
			}
			services, _ := doc["services"].(map[string]any)
			if keys := slices.Sorted(maps.Keys(doc)); !slices.Equal(keys, []string{"name", "networks", "secrets", "services"}) {
				t.Errorf("top-level keys %q:\n%s", keys, o)
			}
			var want []string
			for _, s := range a.Project.Services {
				want = append(want, s.Name)
			}
			slices.Sort(want)
			if got := slices.Sorted(maps.Keys(services)); !slices.Equal(got, want) {
				t.Errorf("services %q, want %q", got, want)
			}
			for svc, v := range services {
				for k := range v.(map[string]any) {
					if !slices.Contains([]string{"restart", "deploy", "security_opt", "networks"}, k) {
						t.Errorf("%q has %q:\n%s", svc, k, o)
					}
				}
			}
			for _, bad := range []string{"\u0085", "\u2028", "\u2029", "privileged", "include", "/host"} {
				if strings.Contains(string(o), bad) && !(bad == "privileged" && strings.Contains(string(o), `\u0085privileged`)) {
					t.Errorf("the agent's compose file carries %q:\n%s", bad, o)
				}
			}
			u := string(appunit.UnitFile(a))
			for _, line := range strings.Split(strings.TrimSuffix(u, "\n"), "\n") {
				key, _, isKey := strings.Cut(line, "=")
				switch {
				case line == "", strings.HasPrefix(line, "# "), line == "[Unit]", line == "[Service]", line == "[Install]":
				case isKey && slices.Contains([]string{"Description", "Documentation", "Requires", "After", "Wants", "Type", "RuntimeDirectory",
					"RuntimeDirectoryMode", "UMask", "LoadCredentialEncrypted", "ExecStartPre", "ExecStart", "ExecStopPost", "Restart",
					"SuccessExitStatus", "RestartSec", "TimeoutStopSec", "WantedBy", "StandardOutput", "StandardError"}, key):
				default:
					t.Errorf("a line the agent doesn't write: %q", line)
				}
				if strings.HasSuffix(line, "\\") || strings.ContainsAny(line, "\u0085\u2028\u2029\r") {
					t.Errorf("a line that goes on: %q", line)
				}
			}
			if !strings.Contains(u, "\nDescription=HomersLab app everything\n") {
				t.Errorf("the unit's description:\n%s", u)
			}
		})
	}
}
