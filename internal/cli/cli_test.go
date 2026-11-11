package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"net"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"

	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/cli"
	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/manager"
	"github.com/ArjunRachithcf/homerslab/internal/manager/setup"
	"github.com/ArjunRachithcf/homerslab/internal/manager/store"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// running starts an agent and a manager in the test's own directories, and returns the manager's control socket.
// With setUp, first-time setup's built steps are already answered: the Ferreira household, with Inês as its admin.
func running(t *testing.T, setUp ...bool) string {
	t.Helper()
	dir := t.TempDir()
	if len(setUp) > 0 && setUp[0] {
		db, err := store.Open(filepath.Join(dir, "manager.db"), setup.StoreSteps())
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range []struct {
			id      string
			answers map[string]string
		}{{"welcome", nil}, {"you", map[string]string{"name": "Inês Ferreira", "mail": "ines@ferreira.family"}}, {"household", map[string]string{"household": "The Ferreira household"}}} {
			if err := db.CompleteStep(context.Background(), st.id, st.answers, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		db.Close()
	}
	adminLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m, err := manager.New(manager.Config{
		DB:          filepath.Join(dir, "manager.db"),
		AdminPort:   adminLn.Addr().(*net.TCPAddr).Port,
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
	go func() { done <- m.Serve(ctx, setupLn, ctlLn, adminLn) }()
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
