package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"

	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/cli"
	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/manager"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// running starts an agent and a manager in the test's own directories, and returns the manager's control socket.
func running(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	m, err := manager.New(manager.Config{
		DB:          filepath.Join(dir, "manager.db"),
		AgentSocket: agenttest.Start(t, agenttest.Primary()),
		SetupPort:   7020,
		Local:       netip.MustParseAddr("127.0.0.1"),
		LAN:         func() lan.LAN { return lan.LAN{} },
		LANAddress:  func() netip.Addr { return netip.MustParseAddr("192.168.1.20") },
	})
	if err != nil {
		t.Fatal(err)
	}
	setupLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctl := filepath.Join(dir, "manager.sock")
	ctlLn, err := net.Listen("unix", ctl)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Serve(ctx, setupLn, ctlLn, nil) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("manager: %v", err)
		}
		m.Close()
	})
	return ctl
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Main(context.Background(), args, cli.Env{Stdout: &out, Stderr: &errOut, Euid: func() int { return 1000 }})
	return code, out.String(), errOut.String()
}

func TestTheCLIShowsThePrimaryAsItsAgentReportsIt(t *testing.T) {
	testenv.Isolate(t)
	ctl := running(t)

	code, out, errOut := run(t, "node", "--json", "--control-socket", ctl)
	if code != 0 {
		t.Fatalf("homerslab node --json: %d %s", code, errOut)
	}
	rec := new(bytes.Buffer)
	agentResp := serveAgent(t)
	_ = json.Compact(rec, []byte(out))
	if rec.String() != agentResp {
		t.Errorf("node --json:\n got %s\nwant the agent's %s", rec, agentResp)
	}

	code, out, errOut = run(t, "node", "--control-socket", ctl)
	if code != 0 {
		t.Fatalf("homerslab node: %d %s", code, errOut)
	}
	for _, want := range []string{"study-pc", "Fedora Linux 43 (Workstation Edition)", "x86_64", "32 GB", "Samsung SSD 980 PRO 1TB, 1 TB", "WDC WD40EFRX-68N, 4 TB, spinning", "TPM 2.0", "NVIDIA GeForce RTX 3060 Lite Hash Rate"} {
		if !strings.Contains(out, want) {
			t.Errorf("homerslab node doesn't say %q:\n%s", want, out)
		}
	}
}

func serveAgent(t *testing.T) string {
	t.Helper()
	sock := agenttest.Start(t, agenttest.Primary())
	c := agentHTTP(sock)
	resp, err := c.Get("http://agent/v1/node")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	var compact bytes.Buffer
	_ = json.Compact(&compact, b.Bytes())
	return compact.String()
}

var linkRe = regexp.MustCompile(`http://192\.168\.1\.20:7020/setup#k=[A-Za-z0-9_-]{43}`)

func TestSetupLinkPrintsTheLinkAndAQRCodeOfIt(t *testing.T) {
	testenv.Isolate(t)
	ctl := running(t)
	code, out, errOut := run(t, "setup", "link", "--control-socket", ctl)
	if code != 0 {
		t.Fatalf("homerslab setup link: %d %s", code, errOut)
	}
	link := linkRe.FindString(out)
	if link == "" {
		t.Fatalf("no link in:\n%s", out)
	}
	if !strings.Contains(out, "30 minutes") || !strings.Contains(out, "once") || !strings.Contains(out, "http://127.0.0.1:7020/setup#k=") {
		t.Errorf("the link's words should say once, 30 minutes, and this machine's own address:\n%s", out)
	}
	if got := decodeQR(t, out); got != link {
		t.Errorf("the QR code says %q, want the link %q", got, link)
	}
}

func TestSetupLinkSaysWhenTheManagerIsNotRunning(t *testing.T) {
	testenv.Isolate(t)
	code, _, errOut := run(t, "setup", "link", "--control-socket", filepath.Join(t.TempDir(), "none.sock"), "--wait", "100ms")
	if code == 0 || !strings.Contains(errOut, "manager") {
		t.Errorf("with no manager: %d %q, want a failure that names the manager", code, errOut)
	}
}

func TestTheManagerRefusesToRunAsRoot(t *testing.T) {
	testenv.Isolate(t)
	var out, errOut bytes.Buffer
	code := cli.Main(context.Background(), []string{"manager", "--db", filepath.Join(t.TempDir(), "m.db")}, cli.Env{Stdout: &out, Stderr: &errOut, Euid: func() int { return 0 }})
	if code == 0 || !strings.Contains(errOut.String(), "unprivileged") {
		t.Errorf("manager as root: %d %q, want a refusal saying it runs unprivileged", code, errOut.String())
	}
}

func TestUnknownCommandsShowTheUsage(t *testing.T) {
	testenv.Isolate(t)
	for _, args := range [][]string{{}, {"frobnicate"}, {"setup"}, {"setup", "nope"}} {
		code, _, errOut := run(t, args...)
		if code != 2 || !strings.Contains(errOut, "Usage") {
			t.Errorf("%v: %d %q, want 2 and the usage", args, code, errOut)
		}
	}
}

// decodeQR reads the QR code drawn in half blocks back into an image, and decodes it as a phone would.
func decodeQR(t *testing.T, out string) string {
	t.Helper()
	out = regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(out, "")
	var rows [][]rune
	for line := range strings.SplitSeq(out, "\n") {
		if strings.ContainsAny(line, "█▀▄") {
			rows = append(rows, []rune(strings.TrimRight(line, "\n")))
		}
	}
	if len(rows) == 0 {
		t.Fatal("no QR code in the output")
	}
	const scale, border = 6, 8
	width := 0
	for _, r := range rows {
		width = max(width, len(r))
	}
	img := image.NewGray(image.Rect(0, 0, width*scale+2*border*scale, 2*len(rows)*scale+2*border*scale))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for y, r := range rows {
		for x, c := range r {
			top, bottom := c == '█' || c == '▀', c == '█' || c == '▄'
			for dy := range 2 * scale {
				if dy < scale && !top || dy >= scale && !bottom {
					continue
				}
				for dx := range scale {
					img.SetGray(border*scale+x*scale+dx, border*scale+2*y*scale+dy, color.Gray{})
				}
			}
		}
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		t.Fatalf("decoding the QR code: %v\n%s", err, out)
	}
	return res.GetText()
}

func TestSetupLinkGivesTheLinkAsJSONForScripts(t *testing.T) {
	testenv.Isolate(t)
	ctl := running(t)
	code, out, errOut := run(t, "setup", "link", "--json", "--control-socket", ctl)
	if code != 0 {
		t.Fatalf("homerslab setup link --json: %d %s", code, errOut)
	}
	var l struct{ URL, LocalURL string }
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		t.Fatalf("%v in %q", err, out)
	}
	if !linkRe.MatchString(l.URL) || !strings.HasPrefix(l.LocalURL, "http://127.0.0.1:7020/setup#k=") {
		t.Errorf("link %+v", l)
	}
}

// cannedControl is a manager's control socket that answers one path with a body.
func cannedControl(t *testing.T, path string, status int, body string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "manager.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// setup join shows the admin's device key and their first passkey's link in this machine's terminal, each with a QR
// code of itself (#64: never on the LAN page).
func TestSetupJoinShowsTheDeviceKeyAndThePasskeyLinkAsQRCodes(t *testing.T) {
	testenv.Isolate(t)
	const key, link = "DEVICE-KEY-PLACEHOLDER", "https://auth.example.test:9443/if/flow/homerslab-passkey-enrolment/?itoken=PLACEHOLDER"
	body, _ := json.Marshal(map[string]any{"deviceKey": key, "deviceKeyExpiresAt": "2026-11-12T13:30:00Z", "enrolment": link,
		"enrolmentExpiresAt": "2026-11-11T14:00:00Z", "signIn": "https://auth.example.test:9443", "admin": "https://admin.example.test:9443",
		"household": "The Ferreiras", "username": "ines"})
	sock := cannedControl(t, "/api/v1/setup/join", 200, string(body))
	code, out, _ := run(t, "setup", "join", "--control-socket", sock)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	first, second, ok := strings.Cut(out, "2. Then create your passkey")
	if !ok || !strings.Contains(first, key) || !strings.Contains(second, link) || !strings.Contains(second, "https://admin.example.test:9443") {
		t.Fatalf("the output:\n%s", out)
	}
	if got := decodeQR(t, first); got != key {
		t.Errorf("the device key's QR code holds %q", got)
	}
	if got := decodeQR(t, second); got != link {
		t.Errorf("the passkey link's QR code holds %q", got)
	}
	sock = cannedControl(t, "/api/v1/setup/join", 409, `{"error":"lan-half-open"}`)
	if code, _, errOut := run(t, "setup", "join", "--control-socket", sock); code == 0 || !strings.Contains(errOut, "first half") {
		t.Errorf("before the first half is done: %d %s", code, errOut)
	}
}
