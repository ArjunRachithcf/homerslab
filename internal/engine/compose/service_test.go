package compose

import (
	"bytes"
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// In v5.5–v5.7, the SDK's dry run combined with an output or error stream applies the changes for real (Compose SDK
// spike, trap 1). The adapter refuses to make such a service.
func TestADryRunIsNeverGivenAStream(t *testing.T) {
	testenv.Isolate(t)
	cli, err := newCLI("")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for name, o := range map[string]options{
		"output": {dryRun: true, out: &out},
		"error":  {dryRun: true, err: &out},
		"both":   {dryRun: true, out: &out, err: &out},
	} {
		if _, err := newService(cli, o); !errors.Is(err, ErrDryRunWithStreams) {
			t.Errorf("a dry run with the %s stream: %v, want ErrDryRunWithStreams", name, err)
		}
	}
	for name, o := range map[string]options{"a dry run": {dryRun: true}, "a real run with streams": {out: &out, err: &out}} {
		if _, err := newService(cli, o); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Planning reads with a dry run and no stream.
func TestTheReaderIsADryRunWithNoStream(t *testing.T) {
	testenv.Isolate(t)
	if o := readerOptions; !o.dryRun || o.out != nil || o.err != nil {
		t.Errorf("the reader's service is %+v, want a dry run with no stream", o)
	}
}

// Only this adapter imports the Compose SDK, and only service.go sets the SDK's service options, so no other code can
// pair a dry run with a stream (Compose SDK spike, pinning rule 5).
func TestOnlyTheAdapterUsesTheSDK(t *testing.T) {
	testenv.Isolate(t)
	root := filepath.Join("..", "..", "..")
	here, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "spikes" || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		dir, err := filepath.Abs(filepath.Dir(path))
		if err != nil {
			return err
		}
		inAdapter := dir == here
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(p, "github.com/docker/compose/") && !inAdapter {
				t.Errorf("%s imports %s: only internal/engine/compose may", path, p)
			}
			if p == "github.com/docker/compose/v5/pkg/compose" && inAdapter && filepath.Base(path) != "service.go" && !strings.HasSuffix(path, "_test.go") {
				t.Errorf("%s imports the SDK's service options: only service.go may", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("service.go"); err != nil {
		t.Fatal(err)
	}
}

// Deep flow nesting is refused before the YAML parser walks it; brackets in quotes and comments don't count.
func TestDeepFlowNestingIsRefusedBeforeParsing(t *testing.T) {
	testenv.Isolate(t)
	deep := strings.Repeat("[", 9000) + strings.Repeat("]", 9000)
	if err := shallow([]byte("x: " + deep)); !errors.Is(err, ErrNotPlain) {
		t.Errorf("9000 deep: %v, want ErrNotPlain", err)
	}
	for _, s := range []string{"x: '" + deep + "'", `x: "` + deep + `"`, "# " + deep + "\nx: 1", "x: " + strings.Repeat("[", 64) + strings.Repeat("]", 64)} {
		if err := shallow([]byte(s)); err != nil {
			t.Errorf("%.20s…: %v", s, err)
		}
	}
}

// Behind plain(), the load itself reads no other file and keeps nothing out of sight: extends and includes are never
// followed, and a project with a service it extends or one a profile leaves out is refused.
func TestTheLoadRefusesWhatPlainWouldHaveCaught(t *testing.T) {
	testenv.Isolate(t)
	secret := filepath.Join(t.TempDir(), "secret.yaml")
	if err := os.WriteFile(secret, []byte("services: {a: {image: 'from-the-secret-file'}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(NoEngine)
	if err != nil {
		t.Fatal(err)
	}
	for name, file := range map[string]string{
		"extends":  "services: {web: {image: x, extends: {file: " + secret + ", service: a}}}\n",
		"include":  "include: [" + secret + "]\nservices: {web: {image: x}}\n",
		"profiles": "services: {web: {image: x}, extra: {image: y, profiles: [later]}}\n",
	} {
		p, err := r.load(context.Background(), "homerslab-example", []byte(file))
		if err == nil {
			for _, s := range p.Services {
				if s.Name != "web" {
					t.Errorf("%s: the load brought in %s (%s)", name, s.Name, s.Image)
				}
			}
			if name != "include" {
				t.Errorf("%s: loaded %+v, want it refused", name, p)
			}
		} else if strings.Contains(err.Error(), "from-the-secret-file") {
			t.Errorf("%s: the secret file's content reached the error: %v", name, err)
		}
	}
}
