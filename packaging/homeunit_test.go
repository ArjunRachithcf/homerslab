package packaging_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/home"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The stand-ins for the privileged sockets a machine holds where Home could look: in /run, as NetBird's
// /run/netbird.sock or the system bus are, and under /var/lib; and an abstract one, which no folder holds. Each is
// world-writable, as those are.
const (
	standInRun      = "/run/homerslab-test-stand-in.sock"
	standInVarLib   = "/var/lib/homerslab-test-stand-in/stand-in.sock"
	standInAbstract = "@homerslab-test-stand-in"
	// probeEnv carries the probe's targets into the sandboxed probe, and marks it as one.
	probeEnv = "HOMERSLAB_TEST_HOME_PROBE"
	// probeSecret is the stand-in for Home's client secret: a placeholder, never a secret.
	probeSecret = "placeholder-home-client-secret"
)

// Home's unit, as systemd runs it (#21, story 69): as root, on a machine with the packages' users and HomersLab at
// /usr/bin/homerslab (CI's home-unit job), with HOMERSLAB_TEST_HOME_UNIT=1. Under the unit's own settings a probe
// can't connect to a world-writable Unix socket in /run, under /var/lib or in the abstract namespace, and still reads
// the projection and its credential; each of the two fences holds alone, and without both the probe connects. Home
// itself, socket-activated under the same settings, answers on the socket systemd hands it.
func TestHomesUnitReachesNoUnixSocket(t *testing.T) {
	testenv.Isolate(t)
	if os.Getenv("HOMERSLAB_TEST_HOME_UNIT") == "" {
		t.Skip("runs as root under systemd, with HOMERSLAB_TEST_HOME_UNIT=1 (CI's home-unit job)")
	}
	if os.Geteuid() != 0 {
		t.Fatal("Home's unit runs only as root: it starts units of its own")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(self, "/usr/") {
		t.Fatalf("the probe is this test, at %s: install it under /usr, where Home's unit can see it", self)
	}
	if _, err := os.Stat("/usr/bin/homerslab"); err != nil {
		t.Fatalf("Home's unit runs /usr/bin/homerslab: %v", err)
	}
	group, err := user.LookupGroup("homerslab-home")
	if err != nil {
		t.Fatalf("Home's group, from the packages' sysusers.d: %v", err)
	}
	gid, _ := strconv.Atoi(group.Gid)

	// The stand-ins, each counting the connections it accepts.
	accepted := map[string]chan struct{}{}
	for _, path := range []string{standInRun, standInVarLib, standInAbstract} {
		if !strings.HasPrefix(path, "@") {
			if _, err := os.Lstat(path); err == nil {
				t.Fatalf("%s is there already", path)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		if !strings.HasPrefix(path, "@") {
			if err := os.Chmod(path, 0o666); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Remove(path) })
		}
		ch := make(chan struct{}, 16)
		accepted[path] = ch
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				c.Close()
				ch <- struct{}{}
			}
		}()
	}
	t.Cleanup(func() { os.Remove(filepath.Dir(standInVarLib)) })

	// The projection, where the unit binds it in, Home's group's to read; on a machine with no HomersLab of its own.
	const projections = "/run/homerslab-projection"
	if _, err := os.Lstat(projections); err == nil {
		t.Fatalf("%s is there already: this machine has a HomersLab of its own", projections)
	}
	if err := os.Mkdir(projections, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(projections) })
	projection := filepath.Join(projections, "home.json")
	if err := home.Write(projection, home.Projection{Household: "Ferreira", Home: "https://home.example.test", MadeAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{projections, projection} {
		if err := os.Chown(p, 0, gid); err != nil {
			t.Fatal(err)
		}
	}

	// Its credential, sealed by systemd as the agent seals it, from a placeholder.
	cred := filepath.Join(t.TempDir(), "manager-home-client-secret.cred")
	seal := exec.Command("systemd-creds", "encrypt", "--name=manager-home-client-secret", "-", cred)
	seal.Stdin = strings.NewReader(probeSecret)
	if out, err := seal.CombinedOutput(); err != nil {
		t.Fatalf("systemd-creds: %v %s", err, out)
	}

	service := unit(t, "homerslab-home.service")
	probe := func(t *testing.T, change func(map[string][]string)) map[string]string {
		t.Helper()
		u := cloned(service)
		u["LoadCredentialEncrypted"] = []string{"manager-home-client-secret:" + cred}
		if change != nil {
			change(u)
		}
		targets, _ := json.Marshal([]string{standInRun, standInVarLib, standInAbstract})
		args := append([]string{"--quiet", "--wait", "--pipe", "--collect", "--setenv=" + probeEnv + "=" + string(targets)},
			properties(u)...)
		args = append(args, self, "-test.run=^TestHomesUnitProbe$", "-test.count=1")
		out, err := exec.Command("systemd-run", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("the probe under Home's unit: %v\n%s", err, out)
		}
		got := map[string]string{}
		for line := range strings.Lines(string(out)) {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok && strings.HasPrefix(k, "probe ") {
				got[strings.TrimPrefix(k, "probe ")] = v
			}
		}
		if len(got) == 0 {
			t.Fatalf("the probe said nothing:\n%s", out)
		}
		return got
	}
	refused := func(t *testing.T, got map[string]string, paths ...string) {
		t.Helper()
		for _, p := range paths {
			if got[p] == "connected" {
				t.Errorf("under Home's unit, the probe connected to %s", p)
			}
			select {
			case <-accepted[p]:
				t.Errorf("%s accepted a connection from Home's unit", p)
			default:
			}
		}
	}
	withUnix := func(u map[string][]string) { u["RestrictAddressFamilies"] = []string{"AF_UNIX AF_INET AF_INET6"} }
	seeingAll := func(u map[string][]string) {
		delete(u, "TemporaryFileSystem")
		delete(u, "BindReadOnlyPaths")
	}

	t.Run("the unit as it is", func(t *testing.T) {
		got := probe(t, nil)
		refused(t, got, standInRun, standInVarLib, standInAbstract)
		if got["projection"] != "read" {
			t.Errorf("Home's unit can't read the projection: %s", got["projection"])
		}
		if got["credential"] != "read" {
			t.Errorf("Home's unit can't read its credential: %s", got["credential"])
		}
		if got["user"] != "homerslab-home" {
			t.Errorf("the probe ran as %s, want homerslab-home", got["user"])
		}
	})
	t.Run("its view alone hides the sockets in folders", func(t *testing.T) {
		got := probe(t, withUnix)
		refused(t, got, standInRun, standInVarLib)
		if got[standInAbstract] != "connected" { // the probe can connect: an abstract socket is in no folder
			t.Errorf("with AF_UNIX, the probe didn't reach the abstract stand-in: %s", got[standInAbstract])
		}
		<-accepted[standInAbstract]
	})
	t.Run("its address families alone refuse every Unix socket", func(t *testing.T) {
		refused(t, probe(t, seeingAll), standInRun, standInVarLib, standInAbstract)
	})
	t.Run("without either the stand-ins are open to it", func(t *testing.T) {
		got := probe(t, func(u map[string][]string) { withUnix(u); seeingAll(u) })
		for _, p := range []string{standInRun, standInVarLib, standInAbstract} {
			if got[p] != "connected" {
				t.Errorf("the stand-in %s isn't open to Home's user, so the fences prove nothing: %s", p, got[p])
			} else {
				<-accepted[p]
			}
		}
	})

	// Home itself, under its unit's settings, on a socket systemd hands it: no AF_UNIX of its own needed.
	t.Run("Home answers on its activated socket", func(t *testing.T) {
		const sock, name = "/run/homerslab-test-home.sock", "homerslab-test-home"
		u := cloned(service)
		u["LoadCredentialEncrypted"] = []string{"manager-home-client-secret:" + cred}
		args := []string{"--quiet", "--unit=" + name, "--socket-property=ListenStream=" + sock,
			"--socket-property=SocketMode=0600", "--socket-property=FileDescriptorName=home"}
		args = append(append(args, properties(u)...), strings.Fields(one(t, service, "ExecStart"))...)
		if out, err := exec.Command("systemd-run", args...).CombinedOutput(); err != nil {
			t.Fatalf("systemd-run: %v %s", err, out)
		}
		t.Cleanup(func() {
			_ = exec.Command("systemctl", "stop", name+".socket", name+".service").Run()
			_ = exec.Command("systemctl", "reset-failed", name+".socket", name+".service").Run()
		})
		c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			}}}
		// Signed out, its API says so: an answer only past its own name, from the projection (503 without it, 421 for
		// another name), and with no built pages needed.
		resp, err := c.Get("http://home.example.test/api/v1/home")
		if err != nil {
			out, _ := exec.Command("journalctl", "--no-pager", "-n", "40", "-u", name+".service").CombinedOutput()
			t.Fatalf("Home on its activated socket: %v\n%s", err, out)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "signed-out") {
			t.Errorf("Home's API, signed out: %s %s, want 401 signed-out (it read the projection, under its unit)", resp.Status, body)
		}
	})
}

// The probe, inside Home's unit: it tries each target and says what happened, one line each.
func TestHomesUnitProbe(t *testing.T) {
	testenv.Isolate(t)
	raw := os.Getenv(probeEnv)
	if raw == "" {
		t.Skip("the probe runs only inside Home's unit, from TestHomesUnitReachesNoUnixSocket")
	}
	var targets []string
	if err := json.Unmarshal([]byte(raw), &targets); err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	for _, p := range targets {
		c, err := net.DialTimeout("unix", p, 2*time.Second)
		if err == nil {
			c.Close()
			fmt.Fprintf(w, "probe %s\tconnected\n", p)
		} else {
			fmt.Fprintf(w, "probe %s\t%v\n", p, err)
		}
	}
	if _, err := home.Read("/run/homerslab-projection/home.json"); err != nil {
		fmt.Fprintf(w, "probe projection\t%v\n", err)
	} else {
		fmt.Fprintf(w, "probe projection\tread\n")
	}
	b, err := os.ReadFile(filepath.Join(os.Getenv("CREDENTIALS_DIRECTORY"), "manager-home-client-secret"))
	switch {
	case err != nil:
		fmt.Fprintf(w, "probe credential\t%v\n", err)
	case string(b) != probeSecret:
		fmt.Fprintf(w, "probe credential\tnot the one sealed\n")
	default:
		fmt.Fprintf(w, "probe credential\tread\n")
	}
	if u, err := user.Current(); err == nil {
		fmt.Fprintf(w, "probe user\t%s\n", u.Username)
	} else {
		fmt.Fprintf(w, "probe user\t%v\n", err)
	}
}

// cloned copies a unit's settings.
func cloned(u map[string][]string) map[string][]string {
	out := make(map[string][]string, len(u))
	for k, v := range u {
		out[k] = slices.Clone(v)
	}
	return out
}

// properties are a unit's settings as systemd-run takes them, without what makes it the installed service: what it
// runs, its socket and its restarts.
func properties(u map[string][]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(u)) {
		if slices.Contains([]string{"ExecStart", "Sockets", "Restart", "RestartSec"}, k) {
			continue
		}
		for _, v := range u[k] {
			out = append(out, "--property="+k+"="+v)
		}
	}
	return out
}
