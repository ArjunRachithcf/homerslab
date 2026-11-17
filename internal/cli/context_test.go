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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ArjunRachithcf/homeward/internal/cli"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// at is setup as the control socket shows it, with the step waiting for secret, at revision 17, answered from the LAN.
func at(step, secret string, answers string) string {
	return `{"revision":17,"steps":[{"id":"` + step + `","state":"waiting","secret":"` + secret + `"}],` +
		`"answers":{"household":"The Ferreira household","answeredFrom:` + step + `":"192.168.1.42","answeredAt:` + step + `":"2026-11-10T13:30:00Z"` + answers + `}}`
}

// answeredAt is how the terminal says when the step was answered: in this machine's time.
var answeredAt = time.Date(2026, 11, 10, 13, 30, 0, 0, time.UTC).Local().Format("15:04 MST on 2 January 2006")

// headerControl is routedControl that also records each request's If-Match.
func headerControl(t *testing.T, answers map[string][2]string) (string, *[]string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "manager.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("If-Match")+" "+string(b))
		a, ok := answers[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		code := http.StatusOK
		if a[0] != "" {
			code = map[string]int{"400": 400, "409": 409, "502": 502}[a[0]]
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(a[1]))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return sock, &got
}

// Before it reads a secret, the terminal says where it goes, for which household, and which device gave the answers
// that decide that, and when; then it sends the secret bound to those answers (#64).
func TestSetupSecretSaysWhereTheSecretGoesAndWhoseAnswersSayIt(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct {
		name, step, answers string
		where               string
	}{
		{"mesh", "network", `,"meshControlPlane":"cloud","meshUrl":"https://api.netbird.io"`, "This token goes to NetBird's cloud, at https://api.netbird.io,"},
		{"mesh", "network", `,"meshControlPlane":"self-hosted","meshUrl":"https://nb.example.org"`, "This token goes to your own NetBird server, at https://nb.example.org,"},
		{"dns", "domain", `,"domain":"example.test","dnsProvider":"infomaniak"`, "This token is for example.test, and goes to Infomaniak's API,"},
		{"mail", "mail", ``, "This password goes to the mail server the setup page names,"},
	} {
		sock, got := headerControl(t, map[string][2]string{
			"GET /api/v1/setup":                   {"", at(c.step, c.name, c.answers)},
			"PUT /api/v1/setup/secrets/" + c.name: {"", at(c.step, c.name, c.answers)},
		})
		code, out, errOut := runWith(t, token+"\n", "setup", "secret", c.name, "--control-socket", sock)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", c.name, code, errOut)
		}
		for _, want := range []string{c.where, "for The Ferreira household,", "as the setup page answered from 192.168.1.42 at " + answeredAt + "."} {
			if !strings.Contains(errOut, want) {
				t.Errorf("%s: the context lacks %q:\n%s", c.name, want, errOut)
			}
		}
		if len(*got) != 2 || !strings.HasPrefix((*got)[1], "PUT /api/v1/setup/secrets/"+c.name+` "17" `) {
			t.Fatalf("%s: requests %q, want the secret sent at the revision shown", c.name, *got)
		}
		var body struct{ Value string }
		_ = json.Unmarshal([]byte(strings.SplitN((*got)[1], " ", 4)[3]), &body)
		if body.Value != token || strings.Contains(out+errOut, token) {
			t.Errorf("%s: sent the secret %v, printed it %v", c.name, body.Value == token, strings.Contains(out+errOut, token))
		}
	}
}

// A secret no step waits for is never read: the terminal says what to do on the setup page first. Nor is one while
// another for the same step is being checked.
func TestSetupSecretReadsNothingNoStepWaitsFor(t *testing.T) {
	testenv.Isolate(t)
	for name, want := range map[string]string{
		"mesh": "choose your mesh on the setup page first",
		"dns":  "give your domain on the setup page first",
		"mail": "give the mail server on the setup page first",
	} {
		sock, got := headerControl(t, map[string][2]string{"GET /api/v1/setup": {"", `{"revision":3,"steps":[{"id":"you","state":"done"}],"answers":{}}`}})
		code, _, errOut := runWith(t, token+"\n", "setup", "secret", name, "--control-socket", sock)
		if code != 1 || !strings.Contains(errOut, want) || strings.Contains(errOut, "isn't shown") {
			t.Errorf("%s with no step waiting: exit %d %q", name, code, errOut)
		}
		if len(*got) != 1 {
			t.Errorf("%s: requests %q", name, *got)
		}
	}
	sock, got := headerControl(t, map[string][2]string{"GET /api/v1/setup": {"", `{"revision":3,"steps":[{"id":"domain","state":"checking","secret":"dns"}],"answers":{}}`}})
	if code, _, errOut := runWith(t, token+"\n", "setup", "secret", "dns", "--control-socket", sock); code != 1 || !strings.Contains(errOut, "being checked now") || len(*got) != 1 {
		t.Errorf("while another is checked: exit %d %q, requests %q", code, errOut, *got)
	}
}

// The answers changed on the page after the terminal showed them: nothing was sent anywhere, and it says so.
func TestSetupSecretSaysWhenTheAnswersChangedMeanwhile(t *testing.T) {
	testenv.Isolate(t)
	sock, _ := headerControl(t, map[string][2]string{
		"GET /api/v1/setup":              {"", at("network", "mesh", `,"meshControlPlane":"cloud","meshUrl":"https://api.netbird.io"`)},
		"PUT /api/v1/setup/secrets/mesh": {"409", `{"error":"stale"}`},
	})
	code, _, errOut := runWith(t, token+"\n", "setup", "secret", "mesh", "--control-socket", sock)
	if code != 1 || !strings.Contains(errOut, "the answers on the setup page changed after this command showed them") || strings.Contains(errOut, token) {
		t.Errorf("exit %d %q", code, errOut)
	}
}

// On a terminal, the command asks for a yes before it reads the secret; anything else sends nothing.
func TestSetupSecretOnATerminalAsksForAYesFirst(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct {
		answer string
		code   int
		sent   int
	}{{"no", 1, 0}, {"", 1, 0}, {"yes", 0, 1}, {"y", 0, 1}, {token, 1, 0}} {
		sock, got := headerControl(t, map[string][2]string{
			"GET /api/v1/setup":              {"", at("network", "mesh", `,"meshControlPlane":"cloud","meshUrl":"https://api.netbird.io"`)},
			"PUT /api/v1/setup/secrets/mesh": {"", `{}`},
		})
		master, tty := openPTY(t)
		var errOut bytes.Buffer
		done := make(chan int, 1)
		go func() {
			done <- cli.Main(context.Background(), []string{"setup", "secret", "mesh", "--control-socket", sock},
				cli.Env{Stdin: tty, Stdout: io.Discard, Stderr: &errOut, Euid: func() int { return 0 }})
		}()
		waitForNoEcho(t, tty) // the yes is read without echo too: a token pasted a step early isn't shown
		if _, err := master.WriteString(c.answer + "\n"); err != nil {
			t.Fatal(err)
		}
		if c.sent > 0 {
			waitForNoEcho(t, tty)
			if _, err := master.WriteString(token + "\n"); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case code := <-done:
			if code != c.code {
				t.Errorf("answering %q: exit %d, want %d: %s", c.answer, code, c.code, errOut.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("answering %q: the command didn't end", c.answer)
		}
		if tio, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS); err != nil || tio.Lflag&unix.ECHO == 0 {
			t.Errorf("answering %q: the terminal was left without echo", c.answer)
		}
		if shown := readAll(master); strings.Contains(shown, "FROMTHETERMINAL") || strings.Contains(errOut.String(), "FROMTHETERMINAL") {
			t.Errorf("answering %q: the token was shown: %q", "the token", shown+errOut.String())
		}
		puts := 0
		for _, r := range *got {
			if strings.HasPrefix(r, "PUT ") {
				puts++
			}
		}
		if puts != c.sent || !strings.Contains(errOut.String(), "Send it there? Type yes: ") {
			t.Errorf("answering %q: sent %d, stderr %q", c.answer, puts, errOut.String())
		}
	}
}

// waitForQuestion waits until stderr shows question, the command then in its read ("": its first question).
// Echo is off from the command's start (#263), so echo alone doesn't say the command got that far.
func waitForQuestion(t *testing.T, stderr *typesAt, question string) {
	t.Helper()
	for range 1000 {
		out := stderr.String()
		if question != "" && strings.Contains(out, question) || question == "" && (strings.Contains(out, "Send it there?") || strings.Contains(out, "it isn't shown as you paste it")) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never asked %q: %q", question, stderr.String())
}

// readAll is what the terminal showed so far: everything its master side can read now, without blocking.
func readAll(master *os.File) string {
	time.Sleep(50 * time.Millisecond)
	fd := int(master.Fd())
	_ = unix.SetNonblock(fd, true)
	defer func() { _ = unix.SetNonblock(fd, false) }()
	var out []byte
	for {
		buf := make([]byte, 4096)
		n, err := unix.Read(fd, buf)
		if n <= 0 || err != nil {
			return string(out)
		}
		out = append(out, buf[:n]...)
	}
}

// waitForNoEcho waits until the command has turned the terminal's echo off: it is reading the secret.
func waitForNoEcho(t *testing.T, tty interface{ Fd() uintptr }) {
	t.Helper()
	for range 400 {
		if tio, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS); err == nil && tio.Lflag&unix.ECHO == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the command never turned echo off")
}

// Echo stays off from the yes to the secret: a token typed or pasted at any moment in between, as the yes is read or
// as the secret's prompt appears, is never shown (#243). The person types it right then, through stderr's writes.
func TestSetupSecretOnATerminalShowsNothingTypedBetweenTheYesAndTheSecret(t *testing.T) {
	testenv.Isolate(t)
	for _, when := range []string{"yes", "Your mesh's token"} {
		sock, got := headerControl(t, map[string][2]string{
			"GET /api/v1/setup":              {"", at("network", "mesh", `,"meshControlPlane":"cloud","meshUrl":"https://api.netbird.io"`)},
			"PUT /api/v1/setup/secrets/mesh": {"", `{}`},
		})
		master, tty := openPTY(t)
		errOut := &typesAt{t: t, when: when, master: master, tty: tty, typed: token + "\n"}
		done := make(chan int, 1)
		go func() {
			done <- cli.Main(context.Background(), []string{"setup", "secret", "mesh", "--control-socket", sock},
				cli.Env{Stdin: tty, Stdout: io.Discard, Stderr: errOut, Euid: func() int { return 0 }})
		}()
		waitForNoEcho(t, tty)
		if _, err := master.WriteString("yes\n"); err != nil {
			t.Fatal(err)
		}
		select {
		case code := <-done:
			if code != 0 {
				t.Fatalf("typing at %q: exit %d: %s", when, code, errOut.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("typing at %q: the command didn't end", when)
		}
		if shown := readAll(master); strings.Contains(shown, "FROMTHETERMINAL") {
			t.Errorf("typing at %q: the token was shown: %q", when, shown)
		}
		if tio, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS); err != nil || tio.Lflag&unix.ECHO == 0 {
			t.Errorf("typing at %q: the terminal was left without echo", when)
		}
		if len(*got) != 2 || !strings.Contains((*got)[1], token) {
			t.Errorf("typing at %q: requests %d, want the token sent", when, len(*got))
		}
	}
}

// typesAt is stderr that types into the terminal once the command writes a line starting with when: the moment a person types. It returns
// only once the terminal has taken the line in, so whether it is shown is decided by the echo of that moment.
type typesAt struct {
	t           *testing.T
	when, typed string
	master, tty *os.File
	mu          sync.Mutex
	buf         bytes.Buffer
	done        bool
}

func (w *typesAt) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if !w.done && strings.HasPrefix(string(p), w.when) {
		w.done = true
		if _, err := w.master.WriteString(w.typed); err != nil {
			return 0, err
		}
		taken := false
		for range 400 {
			if n, err := unix.IoctlGetInt(int(w.tty.Fd()), unix.TIOCINQ); err == nil && n >= len(w.typed) {
				taken = true
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if !taken {
			w.t.Errorf("the terminal never took in what was typed at %q", w.when)
		}
	}
	return len(p), nil
}

func (w *typesAt) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// echoControl is routedControl that also notes, at each PUT, whether tty had its echo back: the secret is read by
// then, and echo isn't kept off while the request runs (up to minutes for a certificate).
func echoControl(t *testing.T, tty *os.File, answers map[string][2]string) (string, *[]string, *atomic.Int32) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "manager.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	var echoAtPut atomic.Int32 // 0: no PUT, 1: echo on, 2: echo off, 3: on, but off after a SIGCONT
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+string(b))
		if r.Method == "PUT" {
			echoAtPut.Store(2)
			if tio, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS); err == nil && tio.Lflag&unix.ECHO != 0 {
				echoAtPut.Store(1)
				// A Ctrl-Z and fg during the request: the command has given echo back, so continuing it must not take
				// echo away again (restore stopped its SIGCONT watch).
				if err := unix.Kill(os.Getpid(), unix.SIGCONT); err != nil {
					t.Error(err)
				}
				for range 40 {
					if tio, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS); err == nil && tio.Lflag&unix.ECHO == 0 {
						echoAtPut.Store(3)
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
		}
		a, ok := answers[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(a[1]))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return sock, &got, &echoAtPut
}

// The terminal has its echo back once the secret is read, before it is sent, and after the command: for setup's
// secret and for `dns token` alike; and after a Ctrl-C at `dns token`'s prompt, with nothing sent (#243).
func TestSecretsOnATerminalGiveEchoBackBeforeSendingAndAfter(t *testing.T) {
	testenv.Isolate(t)
	dnsAnswers := map[string][2]string{
		"GET /api/v1/dns":       {"", `{"domain":"example.test","where":"Infomaniak's API"}`},
		"PUT /api/v1/dns/token": {"", `{"renewed":false}`},
	}
	for _, c := range []struct {
		name    string
		args    []string
		answers map[string][2]string
		typed   []string // a line each time echo is off for a question
		ctrlC   bool
	}{
		{"setup secret", []string{"setup", "secret", "mesh"}, map[string][2]string{
			"GET /api/v1/setup":              {"", at("network", "mesh", `,"meshControlPlane":"cloud","meshUrl":"https://api.netbird.io"`)},
			"PUT /api/v1/setup/secrets/mesh": {"", `{}`},
		}, []string{"yes", token}, false},
		{"dns token", []string{"dns", "token"}, dnsAnswers, []string{dnsTok}, false},
		{"dns token, Ctrl-C", []string{"dns", "token"}, dnsAnswers, nil, true},
	} {
		master, tty := openPTY(t)
		sock, got, echoAtPut := echoControl(t, tty, c.answers)
		ctx, cancel := context.WithCancel(context.Background())
		errOut := &typesAt{t: t, done: true}
		done := make(chan int, 1)
		go func() {
			done <- cli.Main(ctx, append(c.args, "--control-socket", sock),
				cli.Env{Stdin: tty, Stdout: io.Discard, Stderr: errOut, Euid: func() int { return 0 }})
		}()
		waitForQuestion(t, errOut, "")
		for _, line := range c.typed {
			if _, err := master.WriteString(line + "\n"); err != nil {
				t.Fatal(err)
			}
		}
		if c.ctrlC {
			cancel()
		}
		select {
		case code := <-done:
			if want := map[bool]int{false: 0, true: 1}[c.ctrlC]; code != want {
				t.Errorf("%s: exit %d, want %d: %s", c.name, code, want, errOut.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the command didn't end", c.name)
		}
		cancel()
		if tio, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS); err != nil || tio.Lflag&unix.ECHO == 0 {
			t.Errorf("%s: the terminal was left without echo", c.name)
		}
		if shown := readAll(master); strings.Contains(shown, "FROMTHETERMINAL") {
			t.Errorf("%s: the secret was shown: %q", c.name, shown)
		}
		switch e := echoAtPut.Load(); {
		case c.ctrlC && (e != 0 || len(*got) != 1):
			t.Errorf("%s: something was sent: %q", c.name, *got)
		case !c.ctrlC && e == 3:
			t.Errorf("%s: a Ctrl-Z and fg during the PUT turned echo off again: the SIGCONT watch outlived restore", c.name)
		case !c.ctrlC && e != 1:
			t.Errorf("%s: echo at the PUT: %d, want it back on (1) before the secret is sent", c.name, e)
		}
	}
}

// typeInto types line into the terminal and returns once the terminal has taken it in, so whether it is shown is
// decided by the echo of that moment. Only while the command isn't reading: a read empties the queue.
func typeInto(t *testing.T, master, tty *os.File, line string) {
	if _, err := master.WriteString(line); err != nil {
		t.Error(err)
		return
	}
	for range 400 {
		if n, err := unix.IoctlGetInt(int(tty.Fd()), unix.TIOCINQ); err == nil && n >= len(line) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("the terminal never took in %d typed bytes", len(line))
}

// Echo is off from the command's start: a secret pasted while it still asks the manager where it goes is never shown;
// it is taken as the answer, so nothing is sent (#263). The same for `dns token`, whose secret it is. When the command
// stops before it reads it (nothing waits for it), the terminal drops it: the shell never gets it, to show it, run it
// or keep it in its history.
func TestSecretsOnATerminalShowNothingPastedBeforeTheQuestion(t *testing.T) {
	testenv.Isolate(t)
	setup := map[string][2]string{
		"GET /api/v1/setup":              {"", at("network", "mesh", `,"meshControlPlane":"cloud","meshUrl":"https://api.netbird.io"`)},
		"PUT /api/v1/setup/secrets/mesh": {"", `{}`},
	}
	dns := map[string][2]string{
		"GET /api/v1/dns":       {"", `{"domain":"example.test","where":"Infomaniak's API"}`},
		"PUT /api/v1/dns/token": {"", `{"renewed":false}`},
	}
	for _, c := range []struct {
		name, when, secret string // when: "GET" while the manager answers, or the start of a line on stderr
		args               []string
		answers            map[string][2]string
		code, puts         int
	}{
		{"setup secret, during the GET", "GET", token, []string{"setup", "secret", "mesh"}, setup, 1, 0},
		{"setup secret, nothing waiting", "GET", token, []string{"setup", "secret", "mesh"}, map[string][2]string{
			"GET /api/v1/setup": {"", `{"revision":3,"steps":[{"id":"you","state":"done"}],"answers":{}}`},
		}, 1, 0},
		{"dns token, during the GET", "GET", dnsTok, []string{"dns", "token"}, dns, 0, 1},
	} {
		master, tty := openPTY(t)
		sock := filepath.Join(t.TempDir(), "manager.sock")
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		var puts atomic.Int32
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && c.when == "GET" {
				typeInto(t, master, tty, c.secret+"\n")
			}
			if r.Method == "PUT" {
				puts.Add(1)
			}
			a, ok := c.answers[r.Method+" "+r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(a[1]))
		})}
		go func() { _ = srv.Serve(ln) }()
		errOut := &typesAt{t: t, when: c.when, master: master, tty: tty, typed: c.secret + "\n"}
		if c.when == "GET" {
			errOut.done = true
		}
		done := make(chan int, 1)
		go func() {
			done <- cli.Main(context.Background(), append(c.args, "--control-socket", sock),
				cli.Env{Stdin: tty, Stdout: io.Discard, Stderr: errOut, Euid: func() int { return 0 }})
		}()
		select {
		case code := <-done:
			if code != c.code {
				t.Errorf("%s: exit %d, want %d: %s", c.name, code, c.code, errOut.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the command didn't end", c.name)
		}
		srv.Close()
		if shown := readAll(master); strings.Contains(shown, "FROMTHETERMINAL") {
			t.Errorf("%s: the secret was shown: %q", c.name, shown)
		}
		if tio, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS); err != nil || tio.Lflag&unix.ECHO == 0 {
			t.Errorf("%s: the terminal was left without echo", c.name)
		}
		if n, err := unix.IoctlGetInt(int(tty.Fd()), unix.TIOCINQ); err != nil || n != 0 {
			t.Errorf("%s: %d typed bytes left for the shell", c.name, n)
		}
		if int(puts.Load()) != c.puts {
			t.Errorf("%s: sent %d times, want %d", c.name, puts.Load(), c.puts)
		}
	}
}

// A Ctrl-Z and fg while a question waits gives the terminal the shell's modes, echo on: when the command is continued
// it turns echo off again, so what is typed next isn't shown (#263).
func TestSetupSecretOnATerminalTurnsEchoOffAgainWhenContinued(t *testing.T) {
	testenv.Isolate(t)
	sock, got := headerControl(t, map[string][2]string{
		"GET /api/v1/setup":              {"", at("network", "mesh", `,"meshControlPlane":"cloud","meshUrl":"https://api.netbird.io"`)},
		"PUT /api/v1/setup/secrets/mesh": {"", `{}`},
	})
	master, tty := openPTY(t)
	errOut := &typesAt{t: t, done: true} // only kept, safe to read meanwhile
	done := make(chan int, 1)
	go func() {
		done <- cli.Main(context.Background(), []string{"setup", "secret", "mesh", "--control-socket", sock},
			cli.Env{Stdin: tty, Stdout: io.Discard, Stderr: errOut, Euid: func() int { return 0 }})
	}()
	for _, q := range [][2]string{{"Send it there? Type yes: ", "yes"}, {"Your mesh's token", token}} {
		line := q[1]
		// The question is asked: the command is in its read, and turns echo off again only when continued.
		waitForQuestion(t, errOut, q[0])
		// The shell, at fg: its own modes, with echo.
		tio, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS)
		if err != nil {
			t.Fatal(err)
		}
		tio.Lflag |= unix.ECHO
		if err := unix.IoctlSetTermios(int(tty.Fd()), unix.TCSETS, tio); err != nil {
			t.Fatal(err)
		}
		if err := unix.Kill(os.Getpid(), unix.SIGCONT); err != nil {
			t.Fatal(err)
		}
		waitForNoEcho(t, tty)
		if _, err := master.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the command didn't end")
	}
	if shown := readAll(master); strings.Contains(shown, "FROMTHETERMINAL") {
		t.Errorf("the token was shown: %q", shown)
	}
	if len(*got) != 2 {
		t.Errorf("requests %q", *got)
	}
}
