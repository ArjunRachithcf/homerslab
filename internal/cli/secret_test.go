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

func TestSetupSecretSendsTheSecretFromStdinOverTheControlSocket(t *testing.T) {
	testenv.Isolate(t)
	sock, got := fakeControl(t, http.StatusOK, `{"half":"lan","current":"domain"}`)
	code, out, errOut := runWith(t, token+"\n", "setup", "secret", "mesh", "--control-socket", sock)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if len(*got) != 1 {
		t.Fatalf("requests: %v", *got)
	}
	req := strings.SplitN((*got)[0], " ", 4)
	var body struct{ Value string }
	_ = json.Unmarshal([]byte(req[3]), &body)
	if req[0] != "PUT" || req[1] != "/api/v1/setup/secrets/mesh" || req[2] != "application/json" || body.Value != token {
		t.Errorf("sent %s %s %s, value right: %v", req[0], req[1], req[2], body.Value == token)
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
		sock, _ := fakeControl(t, http.StatusBadRequest, `{"error":"`+code+`"}`)
		exit, out, errOut := runWith(t, token, "setup", "secret", "mesh", "--control-socket", sock)
		if exit != 1 || !strings.Contains(errOut, want) || strings.Contains(out+errOut, token) {
			t.Errorf("%s: exit %d, %q", code, exit, errOut)
		}
	}
}

func TestSetupSecretWantsAKnownNameAndAValue(t *testing.T) {
	testenv.Isolate(t)
	sock, got := fakeControl(t, http.StatusOK, `{}`)
	if code, _, _ := runWith(t, token, "setup", "secret", "--control-socket", sock); code != 2 {
		t.Errorf("no name: exit %d, want 2", code)
	}
	if code, _, _ := runWith(t, token, "setup", "secret", "wifi", "--control-socket", sock); code != 2 {
		t.Errorf("an unknown name: exit %d, want 2", code)
	}
	if code, _, errOut := runWith(t, "\n", "setup", "secret", "mesh", "--control-socket", sock); code != 1 || !strings.Contains(errOut, "nothing was read") {
		t.Errorf("an empty line: exit %d %q", code, errOut)
	}
	if len(*got) != 0 {
		t.Errorf("sent %v", *got)
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
	sock, got := fakeControl(t, http.StatusOK, `{}`)
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
	// Type once the CLI has asked: it turns echo off before it prompts... on stderr, which isn't the terminal here,
	// so wait for echo to go off.
	for range 200 {
		if tio, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS); err == nil && tio.Lflag&unix.ECHO == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
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
	if len(*got) != 1 || !strings.Contains((*got)[0], token) {
		t.Errorf("sent %d requests", len(*got))
	}
}
