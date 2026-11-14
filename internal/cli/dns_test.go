package cli_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const dnsTok = "ik_FROMTHETERMINAL00000000000000000000000000000000000"

// routedControl is a manager's control socket that answers each "METHOD path" as told, and records what it was sent.
func routedControl(t *testing.T, answers map[string][2]string) (string, *[]string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "manager.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+string(b))
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

const atDomainStep = `{"revision":9,"steps":[{"id":"domain","state":"waiting","secret":"dns"}],"answers":{"household":"The Ferreira household","domain":"example.test","dnsProvider":"rfc2136","dnsServer":"192.0.2.53:53","dnsKeyName":"homerslab.","householdPort":"9443","adminUrl":"https://admin.example.test:9443"}}`

// Before the DNS token is read, the terminal says where it will go, from the setup's own answers: a stranger who
// claimed the link first can't send it to a server of theirs unseen (#64).
func TestSetupSecretDNSSaysWhereTheTokenGoesThenSendsIt(t *testing.T) {
	testenv.Isolate(t)
	sock, got := routedControl(t, map[string][2]string{
		"GET /api/v1/setup":             {"", atDomainStep},
		"PUT /api/v1/setup/secrets/dns": {"", atDomainStep},
	})
	code, out, errOut := runWith(t, dnsTok+"\n", "setup", "secret", "dns", "--control-socket", sock)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(errOut, "This token is for example.test, and goes to the DNS server 192.0.2.53:53 (RFC 2136), as the key homerslab., for The Ferreira household") {
		t.Errorf("the context: %q", errOut)
	}
	if !strings.Contains(out, "*.example.test is issued") || !strings.Contains(out, "https://admin.example.test:9443") {
		t.Errorf("output: %q", out)
	}
	if len(*got) != 2 || !strings.HasPrefix((*got)[1], "PUT /api/v1/setup/secrets/dns ") {
		t.Fatalf("requests: %v", *got)
	}
	var body struct{ Value string }
	_ = json.Unmarshal([]byte(strings.SplitN((*got)[1], " ", 3)[2]), &body)
	if body.Value != dnsTok || strings.Contains(out+errOut, dnsTok) {
		t.Errorf("sent the token: %v; printed it: %v", body.Value == dnsTok, strings.Contains(out+errOut, dnsTok))
	}
}

func TestSetupSecretDNSSaysWhatToDoWhenRefused(t *testing.T) {
	testenv.Isolate(t)
	for code, want := range map[string]string{
		"secret-refused":    "the DNS provider refused this token",
		"caa-forbids":       "CAA records",
		"no-zone":           "no public DNS zone holds the domain",
		"issuance-failed":   "the certificate wasn't issued",
		"dns-listing-empty": "the DNS provider listed nothing in the zone",
	} {
		sock, _ := routedControl(t, map[string][2]string{
			"GET /api/v1/setup":             {"", atDomainStep},
			"PUT /api/v1/setup/secrets/dns": {"400", `{"error":"` + code + `"}`},
		})
		exit, out, errOut := runWith(t, dnsTok, "setup", "secret", "dns", "--control-socket", sock)
		if exit != 1 || !strings.Contains(errOut, want) || strings.Contains(out+errOut, dnsTok) {
			t.Errorf("%s: exit %d, %q", code, exit, errOut)
		}
	}
	sock, got := routedControl(t, map[string][2]string{"GET /api/v1/setup": {"", `{"revision":4,"steps":[{"id":"domain","state":"todo"}],"answers":{}}`}})
	if exit, _, errOut := runWith(t, dnsTok, "setup", "secret", "dns", "--control-socket", sock); exit != 1 || !strings.Contains(errOut, "give your domain on the setup page first") {
		t.Errorf("before the domain: exit %d %q", exit, errOut)
	}
	if len(*got) != 1 {
		t.Errorf("sent %v before the domain", *got)
	}
}

// `homerslab dns token` replaces the DNS token from the console, outside first-time setup (#80).
func TestDNSTokenReplacesTheTokenFromTheConsole(t *testing.T) {
	testenv.Isolate(t)
	sock, got := routedControl(t, map[string][2]string{
		"GET /api/v1/dns": {"", `{"domain":"example.test","where":"Infomaniak's API","renewAt":"2027-01-10T00:00:00Z",
			"certificate":{"notAfter":"2027-02-09T00:00:00Z","failingSince":"2027-01-10T00:00:00Z","lastError":"the DNS provider refused the token"}}`},
		"PUT /api/v1/dns/token": {"", `{"renewed":true,"certificate":{"notAfter":"2027-04-10T00:00:00Z"}}`},
	})
	code, out, errOut := runWith(t, dnsTok+"\n", "dns", "token", "--control-socket", sock)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"This token is for example.test, and goes to Infomaniak's API.", "expires on 9 February 2027",
		"Renewing it has failed since 10 January 2027"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the context lacks %q: %q", want, errOut)
		}
	}
	if !strings.Contains(out, "The certificate is renewed: it expires on 10 April 2027.") || strings.Contains(out+errOut, dnsTok) {
		t.Errorf("output: %q", out)
	}
	if len(*got) != 2 || !strings.Contains((*got)[1], dnsTok) {
		t.Errorf("requests: %d", len(*got))
	}

	sock, _ = routedControl(t, map[string][2]string{"GET /api/v1/dns": {"409", `{"error":"no-domain"}`}})
	if code, _, errOut := runWith(t, dnsTok, "dns", "token", "--control-socket", sock); code != 1 || !strings.Contains(errOut, "sudo homerslab setup secret dns") {
		t.Errorf("before first-time setup's domain: exit %d %q", code, errOut)
	}
}
