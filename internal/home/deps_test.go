package home_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const module = "github.com/ArjunRachithcf/homerslab"

// privileged are the packages that reach something Home must never reach (story 69, ADR 0013): the manager and its
// database, the agent and the host, the sign-in's and the mesh's APIs with HomersLab's own tokens, the edge's admin
// API and the firewall, the CLI's control socket. Home's process is built from none of them, so no flaw in it can
// call them.
var privileged = []string{
	module + "/internal/manager", module + "/internal/agent", module + "/internal/host", module + "/internal/cli",
	module + "/internal/idp", module + "/internal/mesh/netbird", module + "/internal/mesh/headscale", module + "/internal/edge",
	module + "/internal/firewall", module + "/internal/engine", module + "/internal/dns", module + "/internal/certs",
	module + "/internal/appunit", module + "/internal/node", "database/sql", "os/exec", "syscall", "net/rpc",
}

// imports are the packages a folder's non-test Go files import.
func imports(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range af.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			out = append(out, p)
		}
	}
	return out
}

// Home's code imports nothing privileged: every package it is built from, in this module, and nothing outside the
// standard library and this module.
func TestHomeIsBuiltFromNothingPrivileged(t *testing.T) {
	testenv.Isolate(t)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	todo := []string{module + "/internal/home"}
	for len(todo) > 0 {
		pkg := todo[0]
		todo = todo[1:]
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		for _, p := range imports(t, filepath.Join(root, strings.TrimPrefix(pkg, module))) {
			for _, bad := range privileged {
				if p == bad || strings.HasPrefix(p, bad+"/") {
					t.Errorf("Home is built from %s (imported by %s)", p, pkg)
				}
			}
			switch {
			case strings.HasPrefix(p, module+"/"):
				todo = append(todo, p)
			case strings.Contains(strings.Split(p, "/")[0], "."):
				t.Errorf("Home is built from %s, outside the standard library (imported by %s)", p, pkg)
			}
		}
	}
	if !seen[module+"/internal/oidc"] || len(seen) < 3 {
		t.Errorf("the walk found only %v", slices.Sorted(func(yield func(string) bool) {
			for k := range seen {
				if !yield(k) {
					return
				}
			}
		}))
	}
}
