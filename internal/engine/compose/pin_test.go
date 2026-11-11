package compose_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The SDK is pinned exactly, from v5.7.0 on, and the modules it is built against come from its own go.mod: nothing
// else may move them, or the engine's behaviour changes under the pin (Compose SDK spike, pinning rules 1, 2 and 8).
func TestTheComposeSDKIsPinnedWithItsOwnModules(t *testing.T) {
	testenv.Isolate(t)
	selected := requires(t, filepath.Join("..", "..", "..", "go.mod")) // go mod tidy keeps every module the build uses here
	sdk := selected["github.com/docker/compose/v5"]
	if sdk == "" || semver.Compare(sdk, "v5.7.0") < 0 {
		t.Fatalf("github.com/docker/compose/v5 is %q, want v5.7.0 or later", sdk)
	}
	// What the pinned SDK's own go.mod requires, from the module cache: the test needs no network.
	cache := strings.TrimSpace(goCmd(t, "env", "GOMODCACHE"))
	own := requires(t, filepath.Join(cache, "cache", "download", "github.com", "docker", "compose", "v5", "@v", sdk+".mod"))
	for _, m := range []string{"github.com/docker/cli", "github.com/moby/moby/client", "github.com/moby/moby/api", "github.com/compose-spec/compose-go/v2"} {
		if own[m] == "" {
			t.Errorf("compose %s doesn't require %s any more: review the pinning rules", sdk, m)
		} else if selected[m] != own[m] {
			t.Errorf("%s is %s, but compose %s is built against %s", m, selected[m], sdk, own[m])
		}
	}
	// Modules the SDK brings, held past known advisories.
	for m, least := range map[string]string{
		"github.com/containerd/containerd/v2": "v2.4.1",  // GHSA-rp3h-jf77-q9p4, GHSA-pg57-6jwg-q645, GHSA-7jxh-36q5-gcqv
		"github.com/klauspost/compress":       "v1.20.2", // GO-2026-6865
		"golang.org/x/net":                    "v0.62.0", // GO-2026-6860, GO-2026-6866
		"golang.org/x/text":                   "v0.3.8",  // GHSA-69ch-w2m2-3vjp
	} {
		if v := selected[m]; v == "" || semver.Compare(v, least) < 0 {
			t.Errorf("%s is %q, want %s or later", m, v, least)
		}
	}
}

// requires is a go.mod file's required modules and their versions.
func requires(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := modfile.ParseLax(path, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range f.Require {
		out[r.Mod.Path] = r.Mod.Version
	}
	return out
}

func goCmd(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("go %s: %v: %s", strings.Join(args, " "), err, stderr.String())
	}
	return string(b)
}
