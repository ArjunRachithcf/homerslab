package edge_test

// Kanidm's group names, against a real Kanidm in a container (#236): the edge reads a person's role from the groups
// oauth2-proxy joins with commas (KanidmGuard), which is unambiguous only while no group's name can hold a comma. Like
// the request matrix, it needs Docker: it runs when HOMEWARD_TEST_DOCKER_HOST names the engine to use. Its container
// and volume are named hltest-236-kanidm (or HOMEWARD_TEST_KANIDM_NAME), labelled, published on 127.0.0.1 only, and
// removed with the volume when it ends.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

const kanidmImage = "kanidm/server:1.12.0@sha256:71b6ff5a9c9c32b632894d8acef5a2afc77941b35a0aa95dcee959fe688d5ce9"

func TestKanidmsGroupNamesHoldNoComma(t *testing.T) {
	testenv.Isolate(t)
	engine := os.Getenv("HOMEWARD_TEST_DOCKER_HOST")
	if engine == "" {
		t.Skip("set HOMEWARD_TEST_DOCKER_HOST to the engine for Kanidm's container")
	}
	name := cmp.Or(os.Getenv("HOMEWARD_TEST_KANIDM_NAME"), "hltest-236-kanidm")
	docker := func(args ...string) (string, error) {
		cmd := exec.Command("docker", args...)
		cmd.Env = append(os.Environ(), "DOCKER_HOST="+engine)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := docker(args...)
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", args[0], err, out)
		}
		return strings.TrimSpace(out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := docker("logs", "--tail", "40", name)
			t.Logf("Kanidm's logs:\n%s", out)
		}
		_, _ = docker("rm", "-f", name, name+"-gen")
		_, _ = docker("volume", "rm", "-f", name)
	})
	dir := t.TempDir()
	conf := filepath.Join(dir, "server.toml")
	if err := os.WriteFile(conf, []byte(`version = "2"
bindaddress = "0.0.0.0:8443"
db_path = "/data/kanidm.db"
tls_chain = "/data/chain.pem"
tls_key = "/data/key.pem"
domain = "idm.example.test"
origin = "https://idm.example.test:8443"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Its evaluation certificate, then the server; the image has no shell, so the configuration is copied in.
	label := []string{"--label", "homerslab-test=edge-kanidm", "-v", name + ":/data", "-e", "KANIDM_CONFIG=/data/server.toml"}
	must("volume", "create", "--label", "homerslab-test=edge-kanidm", name)
	must(append(append([]string{"create", "--name", name + "-gen"}, label...), kanidmImage, "kanidmd", "cert-generate")...)
	must("cp", conf, name+"-gen:/data/server.toml")
	must("start", "-a", name+"-gen")
	must(append(append([]string{"run", "-d", "--name", name, "-p", "127.0.0.1::8443"}, label...), kanidmImage)...)
	port := must("port", name, "8443/tcp")
	if i := strings.LastIndex(port, "\n"); i >= 0 {
		port = port[:i] // the IPv4 one first
	}
	ca := filepath.Join(dir, "ca.pem")
	must("cp", name+":/data/ca.pem", ca)
	pem, err := os.ReadFile(ca)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pem)
	addr := strings.TrimPrefix(port, "0.0.0.0:")
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "idm.example.test"}}}
	call := func(method, path, token string, body any, headers map[string]string) (int, http.Header, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(context.Background(), method, "https://"+addr+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, map[string]any{"error": err.Error()}
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		if out == nil {
			out = map[string]any{"body": string(raw)}
		}
		return resp.StatusCode, resp.Header, out
	}
	for range 100 {
		if status, _, _ := call("GET", "/status", "", nil, nil); status == 200 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	// idm_admin, whose password the recovery prints and nothing logs.
	var recovered struct{ Output, Status string }
	if err := json.Unmarshal([]byte(must("exec", name, "kanidmd", "scripting", "recover-account", "idm_admin")), &recovered); err != nil ||
		recovered.Status != "ok" {
		t.Fatalf("recovering idm_admin: %v", err)
	}
	_, h, _ := call("POST", "/v1/auth", "", map[string]any{"step": map[string]any{"init2": map[string]any{
		"username": "idm_admin", "issue": "token", "privileged": true}}}, nil)
	session := map[string]string{"X-KANIDM-AUTH-SESSION-ID": h.Get("X-Kanidm-Auth-Session-Id")}
	call("POST", "/v1/auth", "", map[string]any{"step": map[string]any{"begin": "password"}}, session)
	_, _, r := call("POST", "/v1/auth", "", map[string]any{"step": map[string]any{"cred": map[string]any{"password": recovered.Output}}}, session)
	state, _ := r["state"].(map[string]any)
	token, _ := state["success"].(string)
	if token == "" {
		t.Fatalf("idm_admin's sign-in: %v", state)
	}
	group := func(name string) int {
		status, _, _ := call("POST", "/v1/group", token, map[string]any{"attrs": map[string]any{"name": []string{name}}}, nil)
		return status
	}
	// The control: a role group's own name is made.
	if status := group("homerslab-adults"); status != 200 {
		t.Fatalf("making homerslab-adults: %d", status)
	}
	rename := func(from, to string) int {
		status, _, _ := call("PUT", "/v1/group/"+from+"/_attr/name", token, []string{to}, nil)
		return status
	}
	// The control: a group renamed to a plain name; then back.
	if group("household") != 200 || rename("household", "household-2") != 200 {
		t.Fatal("making and renaming a household group")
	}
	for _, bad := range []string{"x,homerslab-admins", "homerslab-admins,x", ",homerslab-admins", "x, homerslab-admins"} {
		if status := group(bad); status/100 == 2 {
			t.Errorf("Kanidm made a group named %q: a person in it would read as an admin at the edge", bad)
		} else {
			t.Logf("%q: %d", bad, status)
		}
		// Nor renames one to it (review round 2).
		if status := rename("household-2", bad); status/100 == 2 {
			t.Errorf("Kanidm renamed a group to %q", bad)
		} else {
			t.Logf("renamed to %q: %d", bad, status)
		}
	}
}
