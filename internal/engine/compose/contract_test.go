package compose_test

// The container-engine contract suite (spec: Adapter contracts, Testing Decisions), on Docker: a throwaway engine runs
// two apps' compose projects as their units do (the standard compose command, the catalog's file and the agent's,
// appunit), and the adapter reads their state, health and logs, many projects at once, and nothing that isn't
// HomersLab's. It needs an engine nobody else uses: it runs when HOMERSLAB_TEST_DOCKER_HOST names one (CI's runner, or a
// throwaway Docker in a container), and is skipped otherwise. The code under test reaches only that engine, by its
// endpoint; testenv points DOCKER_HOST at nowhere.

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/appunit"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/engine/compose"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// contractImage is the apps' one image: alpine 3.22, pinned by its index digest.
const contractImage = "docker.io/library/alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f"

// contractApp is a catalog compose file: one service that says hello, with a health check and restart policies of its
// own, publishing nothing.
func contractApp(word string) string {
	return `services:
  web:
    image: ` + contractImage + `
    command: ["sh", "-c", "echo hello from ` + word + `; echo trouble in ` + word + ` >&2; exec sleep infinity"]
    volumes:
      - data:/data
    stop_grace_period: 1s
    # Restart policies of its own, the engine's and swarm's: the agent's file turns both off (#195's review).
    restart: always
    deploy:
      restart_policy:
        condition: any
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      start_period: 1s
volumes:
  data:
`
}

func TestTheContainerEngineContractOnDocker(t *testing.T) {
	testenv.Isolate(t)
	host := os.Getenv("HOMERSLAB_TEST_DOCKER_HOST")
	if host == "" {
		t.Skip("set HOMERSLAB_TEST_DOCKER_HOST to a throwaway engine for the contract suite")
	}
	ctx := context.Background()
	dir := t.TempDir()
	r := reader(t)
	// Two apps, each as its unit runs it: their files where the agent keeps them, the standard compose command.
	apps := map[string]appunit.App{}
	for i, id := range []string{"contract-a", "contract-b"} {
		file := contractApp(id)
		p, err := r.Read(ctx, appunit.Project(id), []byte(file))
		if err != nil {
			t.Fatal(err)
		}
		sub := netip.PrefixFrom(netip.AddrFrom4([4]byte{172, 31, 241, byte(16 * i)}), 28)
		a := appunit.App{ID: id, Product: id, Plan: "p-contract", Project: p, Service: "web",
			Upstream: netip.AddrPortFrom(sub.Addr().Next().Next(), 8080), Subnet: sub, Level: fmt.Sprintf("s0:c%d,c%d", 600+2*i, 601+2*i), Slot: i, Dir: filepath.Join(dir, id),
			SignIn: "auth.example.test", SignInAt: netip.MustParseAddr("172.31.255.241")}
		if err := os.MkdirAll(a.Dir, 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(a.ComposeFile(), []byte(file), 0o600)
		_ = os.WriteFile(a.OverrideFile(), appunit.Override(a), 0o600)
		apps[id] = a
	}
	composeCmd := func(a appunit.App, args ...string) {
		t.Helper()
		cmd := exec.Command("docker", append(a.Compose()[1:], args...)...)
		cmd.Env = append(os.Environ(), "DOCKER_HOST="+host)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	for _, a := range apps {
		t.Cleanup(func() { composeCmd(a, "down", "-v", "--remove-orphans", "-t", "1") })
		composeCmd(a, "up", "--detach", "--wait", "--quiet-pull")
	}
	e, err := compose.NewEngine(host)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("every HomersLab project's containers, with their state and health, many at once", func(t *testing.T) {
		st, err := e.States(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"contract-a", "contract-b"} {
			s, ok := st[appunit.Project(id)]
			if !ok || len(s.Containers) != 1 || s.Containers[0].Service != "web" || s.Containers[0].State != "running" || s.Containers[0].Health != "healthy" {
				t.Errorf("%s: %+v (%v)", id, s, ok)
			}
		}
		for name := range st {
			if !strings.HasPrefix(name, engine.ProjectPrefix) {
				t.Errorf("a project that isn't HomersLab's: %s", name)
			}
		}
	})

	t.Run("the compose files the agent writes run as written: no restart policy, no published port, the upstream at its address", func(t *testing.T) {
		out, err := exec.Command("docker", "-H", host, "inspect", "--format",
			`{{.HostConfig.RestartPolicy.Name}} {{len .NetworkSettings.Ports}} {{range $n, $c := .NetworkSettings.Networks}}{{$n}}={{$c.IPAddress}} {{end}}`,
			appunit.Project("contract-a")+"-web-1").CombinedOutput()
		got := strings.TrimSpace(string(out))
		if err != nil || !strings.HasPrefix(got, "no 0 ") || !strings.Contains(got, "homerslab-contract-a_homerslab-upstream=172.31.241.2") ||
			!strings.Contains(got, "homerslab-contract-a_default=") {
			t.Errorf("inspect: %q %v", got, err)
		}
		// Every network's bridge has the name the agent gave it, which the firewall table knows it by (#82).
		for network, bridge := range map[string]string{"homerslab-contract-a_homerslab-upstream": "hla0u", "homerslab-contract-a_default": "hla0n0",
			"homerslab-contract-b_homerslab-upstream": "hla1u", "homerslab-contract-b_default": "hla1n0"} {
			out, err := exec.Command("docker", "-H", host, "network", "inspect", "--format", `{{index .Options "com.docker.network.bridge.name"}}`, network).CombinedOutput()
			if got := strings.TrimSpace(string(out)); err != nil || got != bridge {
				t.Errorf("%s's bridge: %q %v, want %s", network, got, err, bridge)
			}
		}
	})

	// The claim ADR 0020 tests first: an app's container resolves auth.<main domain> to the sign-in listener on a host
	// running systemd-resolved (CI's runner), whose stub resolver Docker can't hand a container. The agent's file gives
	// it the name in the container's own hosts file, which any resolver reads first.
	t.Run("claim (#82): a container resolves the sign-in's name to the sign-in listener, systemd-resolved or not", func(t *testing.T) {
		resolved := exec.Command("systemctl", "is-active", "--quiet", "systemd-resolved.service").Run() == nil
		t.Logf("this host runs systemd-resolved: %v (the engine's host, when the engine is this machine's)", resolved)
		out, err := exec.Command("docker", "-H", host, "exec", appunit.Project("contract-a")+"-web-1", "getent", "hosts", "auth.example.test").CombinedOutput()
		if f := strings.Fields(string(out)); err != nil || len(f) < 2 || f[0] != "172.31.255.241" || f[1] != "auth.example.test" {
			t.Errorf("getent hosts auth.example.test: %q %v", out, err)
		}
	})

	// What backups read (#22): a service's volumes, where they are on the machine, and a dump's command, which runs in
	// the service through the app's own compose command.
	t.Run("a service's volumes, inside and on the machine, and a command in it through the app's compose command", func(t *testing.T) {
		ms, err := e.Mounts(ctx, appunit.Project("contract-a"))
		if err != nil {
			t.Fatal(err)
		}
		if len(ms["web"]) != 1 || ms["web"][0].Destination != "/data" || !strings.HasSuffix(ms["web"][0].Source, "/homerslab-contract-a_data/_data") {
			t.Errorf("contract-a's mounts: %+v", ms)
		}
		if _, err := e.Mounts(ctx, "hltest-other"); err == nil {
			t.Error("the mounts of a project that isn't HomersLab's")
		}
		a := apps["contract-a"]
		cmd := exec.Command("docker", append(a.Compose()[1:], "exec", "-T", "web", "printf", "the-dump")...)
		cmd.Env = append(os.Environ(), "DOCKER_HOST="+host)
		if out, err := cmd.Output(); err != nil || string(out) != "the-dump" {
			t.Errorf("a dump through the app's compose command: %q %v", out, err)
		}
	})

	t.Run("a project's logs, from its standard output and its standard error", func(t *testing.T) {
		lines, err := e.Logs(ctx, appunit.Project("contract-b"), 50)
		if err != nil {
			t.Fatal(err)
		}
		var said []string
		for _, l := range lines {
			said = append(said, l.Line)
		}
		if !slices.Contains(said, "hello from contract-b") || !slices.Contains(said, "trouble in contract-b") || slices.Contains(said, "hello from contract-a") {
			t.Errorf("contract-b's logs: %q", said)
		}
		if _, err := e.Logs(ctx, "hltest-other", 10); err == nil {
			t.Error("the logs of a project that isn't HomersLab's")
		}
	})

	t.Run("a stopped project is there, and not running", func(t *testing.T) {
		composeCmd(apps["contract-b"], "stop", "-t", "1")
		var s engine.State
		for range 50 {
			st, err := e.States(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if s = st[appunit.Project("contract-b")]; len(s.Containers) == 1 && s.Containers[0].State != "running" {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Errorf("stopped: %+v", s)
	})

	t.Run("a project taken down is gone", func(t *testing.T) {
		composeCmd(apps["contract-b"], "down", "-v", "-t", "1")
		st, err := e.States(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := st[appunit.Project("contract-b")]; ok {
			t.Errorf("after down: %+v", s)
		}
	})

	t.Run("an engine that doesn't answer is an error, never no projects", func(t *testing.T) {
		nowhere, err := compose.NewEngine(compose.NoEngine)
		if err != nil {
			t.Fatal(err)
		}
		short, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if st, err := nowhere.States(short); err == nil {
			t.Errorf("no engine: %+v", st)
		}
	})
}
