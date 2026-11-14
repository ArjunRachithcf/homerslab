package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ArjunRachithcf/homerslab/internal/cli"
	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const token = "nbp_FROMTHETERMINAL000000000000000000000"

func runWith(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Main(context.Background(), args, cli.Env{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errOut, Euid: func() int { return 0 }})
	return code, out.String(), errOut.String()
}

// fakeControl is a manager's control socket that records what it was sent and answers with status and body.
func fakeControl(t *testing.T, status int, answer string) (string, *[]string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "manager.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Content-Type")+" "+string(b))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return sock, &got
}

// atNetworkStep is setup waiting for the mesh's token.
const atNetworkStep = `{"revision":5,"steps":[{"id":"network","state":"waiting","secret":"mesh"}],"answers":{"household":"The Ferreira household","meshControlPlane":"cloud","meshUrl":"https://api.netbird.io"}}`

func TestSetupSecretSendsTheSecretFromStdinOverTheControlSocket(t *testing.T) {
	testenv.Isolate(t)
	sock, got := routedControl(t, map[string][2]string{
		"GET /api/v1/setup":              {"", atNetworkStep},
		"PUT /api/v1/setup/secrets/mesh": {"", `{"half":"lan","current":"domain"}`},
	})
	code, out, errOut := runWith(t, token+"\n", "setup", "secret", "mesh", "--control-socket", sock)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if len(*got) != 2 {
		t.Fatalf("requests: %v", *got)
	}
	req := strings.SplitN((*got)[1], " ", 3)
	var body struct{ Value string }
	_ = json.Unmarshal([]byte(req[2]), &body)
	if req[0] != "PUT" || req[1] != "/api/v1/setup/secrets/mesh" || body.Value != token {
		t.Errorf("sent %s %s, value right: %v", req[0], req[1], body.Value == token)
	}
	if strings.Contains(out+errOut, token) {
		t.Error("the CLI printed the secret")
	}
	if !strings.Contains(out, "Continue on the setup page") {
		t.Errorf("output: %q", out)
	}
}

func TestSetupSecretSaysWhatToDoWhenRefused(t *testing.T) {
	testenv.Isolate(t)
	for code, want := range map[string]string{
		"not-waiting":    "choose your mesh on the setup page first",
		"secret-refused": "the mesh refused this token",
	} {
		sock, _ := routedControl(t, map[string][2]string{
			"GET /api/v1/setup":              {"", atNetworkStep},
			"PUT /api/v1/setup/secrets/mesh": {"400", `{"error":"` + code + `"}`},
		})
		exit, out, errOut := runWith(t, token, "setup", "secret", "mesh", "--control-socket", sock)
		if exit != 1 || !strings.Contains(errOut, want) || strings.Contains(out+errOut, token) {
			t.Errorf("%s: exit %d, %q", code, exit, errOut)
		}
	}
}

func TestSetupSecretWantsAKnownNameAndAValue(t *testing.T) {
	testenv.Isolate(t)
	sock, got := routedControl(t, map[string][2]string{"GET /api/v1/setup": {"", atNetworkStep}})
	if code, _, _ := runWith(t, token, "setup", "secret", "--control-socket", sock); code != 2 {
		t.Errorf("no name: exit %d, want 2", code)
	}
	if code, _, _ := runWith(t, token, "setup", "secret", "wifi", "--control-socket", sock); code != 2 {
		t.Errorf("an unknown name: exit %d, want 2", code)
	}
	if code, _, errOut := runWith(t, "\n", "setup", "secret", "mesh", "--control-socket", sock); code != 1 || !strings.Contains(errOut, "nothing was read") {
		t.Errorf("an empty line: exit %d %q", code, errOut)
	}
	for _, r := range *got {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("sent %v", r)
		}
	}
}

func TestSetupSecretAgainstARealManagerWithNothingWaiting(t *testing.T) {
	testenv.Isolate(t)
	ctl := running(t)
	code, out, errOut := runWith(t, token, "setup", "secret", "mesh", "--control-socket", ctl)
	if code != 1 || !strings.Contains(errOut, "choose your mesh on the setup page first") || strings.Contains(out+errOut, token) {
		t.Errorf("exit %d: %q %q", code, out, errOut)
	}
}

// openPTY is a pseudo-terminal: the side a person types into, and the terminal the CLI reads.
func openPTY(t *testing.T) (master, tty *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals here: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals here: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return m, s
}

func TestSetupSecretOnATerminalNeverEchoesTheSecret(t *testing.T) {
	testenv.Isolate(t)
	sock, got := routedControl(t, map[string][2]string{
		"GET /api/v1/setup":              {"", atNetworkStep},
		"PUT /api/v1/setup/secrets/mesh": {"", `{}`},
	})
	master, tty := openPTY(t)
	before, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS)
	if err != nil || before.Lflag&unix.ECHO == 0 {
		t.Fatalf("the terminal doesn't echo to begin with: %v", err)
	}

	var out, errOut bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- cli.Main(context.Background(), []string{"setup", "secret", "mesh", "--control-socket", sock},
			cli.Env{Stdin: tty, Stdout: &out, Stderr: &errOut, Euid: func() int { return 0 }})
	}()
	// The yes, then the secret once the CLI asks for it: it turns echo off before it prompts... on stderr, which isn't
	// the terminal here, so wait for echo to go off.
	if _, err := master.WriteString("yes\n"); err != nil {
		t.Fatal(err)
	}
	waitForNoEcho(t, tty)
	if _, err := master.WriteString(token + "\n"); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	// What the terminal showed: everything the master side can read now, without blocking.
	time.Sleep(100 * time.Millisecond)
	fd := int(master.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	var echoed []byte
	for {
		buf := make([]byte, 4096)
		n, err := unix.Read(fd, buf)
		if n <= 0 || err != nil {
			break
		}
		echoed = append(echoed, buf[:n]...)
	}
	if bytes.Contains(echoed, []byte("FROMTHETERMINAL")) {
		t.Errorf("the terminal echoed the secret: %q", echoed)
	}
	if !strings.Contains(errOut.String(), "isn't shown") {
		t.Errorf("no prompt: %q", errOut.String())
	}
	if after, _ := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS); after.Lflag&unix.ECHO == 0 {
		t.Error("the terminal was left without echo")
	}
	if len(*got) != 2 || !strings.Contains((*got)[1], token) {
		t.Errorf("sent %d requests", len(*got))
	}
}

func TestCtrlCAtThePromptStopsAndLeavesTheTerminalAsItWas(t *testing.T) {
	testenv.Isolate(t)
	sock, got := routedControl(t, map[string][2]string{"GET /api/v1/setup": {"", atNetworkStep}})
	master, tty := openPTY(t)
	ctx, cancel := context.WithCancel(context.Background())
	var errOut bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- cli.Main(ctx, []string{"setup", "secret", "mesh", "--control-socket", sock},
			cli.Env{Stdin: tty, Stdout: io.Discard, Stderr: &errOut, Euid: func() int { return 0 }})
	}()
	if _, err := master.WriteString("yes\n"); err != nil {
		t.Fatal(err)
	}
	waitForNoEcho(t, tty)
	cancel() // Ctrl-C
	select {
	case code := <-done:
		if code != 1 {
			t.Errorf("exit %d after Ctrl-C", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C at the prompt didn't stop the command")
	}
	if tio, _ := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS); tio.Lflag&unix.ECHO == 0 {
		t.Error("the terminal was left without echo")
	}
	if len(*got) != 1 {
		t.Errorf("something was sent: %v", *got)
	}
}

func TestFirewallRestoreLoadsTheSavedTableOrAClosedOne(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	code, out, errOut := runWith(t, "", "firewall", "restore", "--print", "--state-dir", state, "--setup-port", "7444")
	if code != 0 || out != firewall.Closed(7444) {
		t.Fatalf("first boot: %d %q %s", code, out, errOut)
	}
	saved := firewall.Render(firewall.State{Mesh: []string{"wt0"}, LAN: []string{"enp5s0"}})
	if err := firewall.Save(filepath.Join(state, "firewall.nft"), saved); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runWith(t, "", "firewall", "restore", "--print", "--state-dir", state); code != 0 || out != saved {
		t.Errorf("with a saved table: %d %q", code, out)
	}
}

func TestFirewallRestoreLoadsAClosedTableWhenTheSavedOneCantBeUsed(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	saved := firewall.Render(firewall.State{Mesh: []string{"wt0"}, LAN: []string{"enp5s0"}})
	if err := os.WriteFile(filepath.Join(state, "firewall.nft"), []byte(saved[:len(saved)/3]), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runWith(t, "", "firewall", "restore", "--print", "--state-dir", state)
	if code != 0 || out != firewall.Closed(7020) || !strings.Contains(errOut, "closed") {
		t.Errorf("a truncated table: exit %d, stderr %q, loads:\n%s", code, errOut, out)
	}
}

// The closed table's LAN exception is the manager's setup port, as the manager last gave it to the agent; none once
// the listener closed for good.
func TestFirewallRestoreTakesTheManagersSetupPort(t *testing.T) {
	testenv.Isolate(t)
	for name, c := range map[string]struct {
		state string
		want  int
	}{
		"the manager's port":                         {`{"enforcing":false,"setupPort":7444}`, 7444},
		"closed for good":                            {`{"enforcing":true,"setupClosed":true,"setupPort":7444}`, 0},
		"no port given yet":                          {`{"enforcing":false}`, 7020},
		"an agent state that isn't one: the default": {``, 7020},
	} {
		state := t.TempDir()
		if err := os.WriteFile(filepath.Join(state, "agent.json"), []byte(c.state), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, out, errOut := runWith(t, "", "firewall", "restore", "--print", "--state-dir", state); code != 0 || out != firewall.Closed(c.want) {
			t.Errorf("%s: exit %d %s, loads:\n%s", name, code, errOut, out)
		}
	}
	// A state that can't be read for now (an I/O error) may say "closed for good": no LAN exception then.
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(state, "agent.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := runWith(t, "", "firewall", "restore", "--print", "--state-dir", state); code != 0 || out != firewall.Closed(0) {
		t.Errorf("an agent state that can't be read: exit %d %s, loads:\n%s", code, errOut, out)
	}
}

func TestMeshOwnerPasswordIsForRootAndWarns(t *testing.T) {
	testenv.Isolate(t)
	sock, got := fakeControl(t, http.StatusOK, `{"password":"owner-PASSWORD-Aa1"}`)
	var out, errOut bytes.Buffer
	if code := cli.Main(context.Background(), []string{"mesh", "owner-password", "--control-socket", sock},
		cli.Env{Stdout: &out, Stderr: &errOut, Euid: func() int { return 1000 }}); code != 1 || len(*got) != 0 {
		t.Errorf("as a user: exit %d, asked %d times", code, len(*got))
	}
	out.Reset()
	errOut.Reset()
	if code := cli.Main(context.Background(), []string{"mesh", "owner-password", "--control-socket", sock},
		cli.Env{Stdout: &out, Stderr: &errOut, Euid: func() int { return 0 }}); code != 0 {
		t.Fatalf("as root: exit %d %s", code, errOut.String())
	}
	if out.String() != "owner-PASSWORD-Aa1\n" || !strings.Contains(errOut.String(), "password manager") || strings.Contains(errOut.String(), "owner-PASSWORD") {
		t.Errorf("stdout %q, stderr %q", out.String(), errOut.String())
	}
	sock, _ = fakeControl(t, http.StatusNotFound, `{"error":"no-owner-password"}`)
	if code, _, errOut := runWith(t, "", "mesh", "owner-password", "--control-socket", sock); code != 1 || !strings.Contains(errOut, "didn't set up this mesh") {
		t.Errorf("without one: %d %q", code, errOut)
	}
}
