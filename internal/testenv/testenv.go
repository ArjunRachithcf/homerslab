// Package testenv keeps every test in its own world: its own home directory, and any container engine pointed at
// nowhere. This machine may run a live household; a test must never reach it.
package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

// Nowhere is where a test's container engine points: a socket that doesn't exist.
const Nowhere = "unix:///nonexistent"

// Isolate gives the test its own empty home directory (with its own XDG directories) and points the Docker and
// Podman clients at Nowhere. Every test calls it first; TestEveryTestIsIsolated checks that they do. It returns
// the home directory.
func Isolate(t testing.TB) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for v, dir := range map[string]string{
		"XDG_CONFIG_HOME": ".config",
		"XDG_DATA_HOME":   ".local/share",
		"XDG_STATE_HOME":  ".local/state",
		"XDG_CACHE_HOME":  ".cache",
		"XDG_RUNTIME_DIR": "run",
		"DOCKER_CONFIG":   ".docker",
	} {
		t.Setenv(v, filepath.Join(home, dir))
	}
	t.Setenv("DOCKER_HOST", Nowhere)
	t.Setenv("CONTAINER_HOST", Nowhere)
	for _, v := range []string{"DOCKER_CONTEXT", "COMPOSE_PROJECT_NAME"} {
		unset(t, v)
	}
	return home
}

// unset removes a variable for the rest of the test, and puts it back afterwards.
func unset(t testing.TB, v string) {
	t.Helper()
	t.Setenv(v, "") // records the old value for the cleanup, and refuses parallel tests
	if err := os.Unsetenv(v); err != nil {
		t.Fatal(err)
	}
}
