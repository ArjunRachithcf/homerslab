package testenv_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

func TestIsolateGivesEachTestItsOwnHome(t *testing.T) {
	home := testenv.Isolate(t)
	if got := os.Getenv("HOME"); got != home {
		t.Fatalf("HOME = %q, want the isolated %q", got, home)
	}
	if real, _ := os.UserHomeDir(); real != home {
		t.Fatalf("os.UserHomeDir() = %q, want %q", real, home)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("the isolated home should exist and be empty: %v, %d entries", err, len(entries))
	}
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		if got := os.Getenv(v); !strings.HasPrefix(got, home) {
			t.Errorf("%s = %q, want it inside %q", v, got, home)
		}
	}
}

func TestIsolatePointsAnyEngineAtNowhere(t *testing.T) {
	testenv.Isolate(t)
	for _, v := range []string{"DOCKER_HOST", "CONTAINER_HOST"} {
		if got := os.Getenv(v); got != testenv.Nowhere {
			t.Errorf("%s = %q, want %q", v, got, testenv.Nowhere)
		}
	}
	if testenv.Nowhere != "unix:///nonexistent" {
		t.Errorf("Nowhere = %q", testenv.Nowhere)
	}
	for _, v := range []string{"DOCKER_CONTEXT", "COMPOSE_PROJECT_NAME"} {
		if got, set := os.LookupEnv(v); set {
			t.Errorf("%s = %q, want it unset", v, got)
		}
	}
	if got := os.Getenv("DOCKER_CONFIG"); !strings.HasPrefix(got, os.Getenv("HOME")) {
		t.Errorf("DOCKER_CONFIG = %q, want it inside the isolated home", got)
	}
}

func TestTwoTestsGetTwoHomes(t *testing.T) {
	outer := testenv.Isolate(t)
	homes := []string{outer}
	for _, name := range []string{"a", "b"} {
		t.Run(name, func(t *testing.T) { homes = append(homes, testenv.Isolate(t)) })
	}
	if len(homes) != 3 || homes[0] == homes[1] || homes[1] == homes[2] || homes[0] == homes[2] {
		t.Fatalf("homes = %v, want three different ones", homes)
	}
}

// Every test in the repository isolates itself first, so none can reach the person's home or a live engine.
func TestEveryTestIsIsolated(t *testing.T) {
	testenv.Isolate(t)
	_, self, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(self), "..", "..")
	var checked int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "spikes") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Name.Name == "TestMain" || fn.Body == nil {
				continue
			}
			checked++
			if !startsWithIsolate(fn) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s: %s must start with testenv.Isolate(t)", rel, fn.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 4 {
		t.Fatalf("checked only %d tests: is the walk looking in the right place (%s)?", checked, root)
	}
}

func startsWithIsolate(fn *ast.FuncDecl) bool {
	if len(fn.Body.List) == 0 {
		return false
	}
	var call *ast.CallExpr
	switch s := fn.Body.List[0].(type) {
	case *ast.ExprStmt:
		call, _ = s.X.(*ast.CallExpr)
	case *ast.AssignStmt:
		if len(s.Rhs) == 1 {
			call, _ = s.Rhs[0].(*ast.CallExpr)
		}
	}
	if call == nil {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Isolate" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testenv"
}
