package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ArjunRachithcf/homerslab/internal/cli"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// at is setup as the control socket shows it, with the step waiting for secret, at revision 17, claimed from the LAN.
func at(step, secret string, answers string) string {
	return `{"revision":17,"claim":{"from":"192.168.1.42","at":"2026-11-10T13:30:00Z"},` +
		`"steps":[{"id":"` + step + `","state":"waiting","secret":"` + secret + `"}],"answers":{"household":"The Ferreira household"` + answers + `}}`
}

// claimedAt is how the terminal says when the setup was claimed: in this machine's time.
var claimedAt = time.Date(2026, 11, 10, 13, 30, 0, 0, time.UTC).Local().Format("15:04 MST on 2 January 2006")

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

// Before it reads a secret, the terminal says where it goes, for which household, and who claimed the setup whose
// answers decide that; then it sends the secret bound to those answers (#64).
func TestSetupSecretSaysWhereTheSecretGoesAndWhoseSetupItIs(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct {
		name, step, answers string
		where               string
	}{
		{"mesh", "network", `,"meshControlPlane":"cloud","meshUrl":"https://api.netbird.io"`, "This token goes to NetBird's cloud, at https://api.netbird.io,"},
		{"mesh", "network", `,"meshControlPlane":"self-hosted","meshUrl":"https://nb.example.org"`, "This token goes to your own NetBird server, at https://nb.example.org,"},
		{"dns", "domain", `,"domain":"example.test","dnsProvider":"infomaniak"`, "This token is for example.test, and goes to Infomaniak's API,"},
		{"mail", "mail", `,"mailServer":"smtp.example.test:587","mailFrom":"homerslab@example.test"`, "This password is for homerslab@example.test, and goes to the mail server smtp.example.test:587,"},
	} {
		sock, got := headerControl(t, map[string][2]string{
			"GET /api/v1/setup":                   {"", at(c.step, c.name, c.answers)},
			"PUT /api/v1/setup/secrets/" + c.name: {"", at(c.step, c.name, c.answers)},
		})
		code, out, errOut := runWith(t, token+"\n", "setup", "secret", c.name, "--control-socket", sock)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", c.name, code, errOut)
		}
		for _, want := range []string{c.where, "for The Ferreira household,", "in the setup claimed from 192.168.1.42 at " + claimedAt + "."} {
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

// A secret no step waits for is never read: the terminal says what to do on the setup page first.
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
	}{{"no", 1, 0}, {"", 1, 0}, {"yes", 0, 1}, {"y", 0, 1}} {
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
