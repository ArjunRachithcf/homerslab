package authentik

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// Authentik is ready for Prepare once its default and system blueprints have applied, and every default flow is
// there. A blueprint whose file an upgrade removed is orphaned: there is nothing left to apply, so it doesn't stop
// Prepare; one that hasn't applied yet, or failed, does.
func TestDefaultsAreReadyOnceTheBlueprintsHaveApplied(t *testing.T) {
	testenv.Isolate(t)
	var blueprints []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var results []any
		switch {
		case r.URL.Path == "/api/v3/flows/instances/":
			results = []any{map[string]string{"pk": "f", "slug": r.URL.Query().Get("slug")}}
		case r.URL.Path == "/api/v3/crypto/certificatekeypairs/":
			results = []any{map[string]string{"pk": "c"}}
		case r.URL.Path == "/api/v3/managed/blueprints/":
			for _, b := range blueprints {
				results = append(results, b)
			}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"pagination": map[string]any{"next": 0}, "results": results})
	}))
	defer srv.Close()
	a, err := newAPI(srv.URL, idp.Household{Name: "x", Domain: "example.test"}, "token-PLACEHOLDER")
	if err != nil {
		t.Fatal(err)
	}
	bp := func(path, status string, enabled bool) map[string]any {
		return map[string]any{"path": path, "status": status, "enabled": enabled}
	}
	for _, c := range []struct {
		name  string
		bps   []map[string]any
		ready bool
	}{
		{"all applied", []map[string]any{bp("default/flow-a.yaml", "successful", true), bp("system/b.yaml", "successful", true)}, true},
		{"one removed by an upgrade (orphaned)", []map[string]any{bp("default/flow-a.yaml", "successful", true), bp("default/flow-gone.yaml", "orphaned", true)}, true},
		{"one not applied yet", []map[string]any{bp("default/flow-a.yaml", "successful", true), bp("default/flow-b.yaml", "unknown", true)}, false},
		{"one failed", []map[string]any{bp("default/flow-a.yaml", "error", true)}, false},
		{"an example, disabled", []map[string]any{bp("default/flow-a.yaml", "successful", true), bp("example/x.yaml", "unknown", false)}, true},
		{"none found yet", nil, false},
		{"only orphaned ones", []map[string]any{bp("default/flow-gone.yaml", "orphaned", true)}, false},
	} {
		blueprints = c.bps
		err := defaultsReady(context.Background(), a)
		if c.ready != (err == nil) || (err != nil && !errors.Is(err, idp.ErrNotReady)) {
			t.Errorf("%s: %v", c.name, err)
		}
		if err != nil && strings.Contains(err.Error(), "token-PLACEHOLDER") {
			t.Errorf("%s: the error holds the token", c.name)
		}
	}
}

// A default blueprint that failed (Authentik's worker applies them in no set order at its first start, and one may come
// before what it builds on) is applied again while the caller waits, at most every so often; one applied just now is
// left to finish.
func TestAFailedDefaultBlueprintIsAppliedAgain(t *testing.T) {
	testenv.Isolate(t)
	var applied []string
	var lastApplied string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var results []any
		switch {
		case r.Method == http.MethodPost:
			applied = append(applied, r.URL.Path)
			_, _ = w.Write([]byte("{}"))
			return
		case r.URL.Path == "/api/v3/flows/instances/":
			results = []any{map[string]string{"pk": "f", "slug": r.URL.Query().Get("slug")}}
		case r.URL.Path == "/api/v3/managed/blueprints/":
			results = []any{map[string]any{"pk": "b-ok", "path": "default/flow-a.yaml", "status": "successful", "enabled": true},
				map[string]any{"pk": "b-oobe", "path": "default/flow-oobe.yaml", "status": "error", "enabled": true, "last_applied": lastApplied}}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"pagination": map[string]any{"next": 0}, "results": results})
	}))
	defer srv.Close()
	a, err := newAPI(srv.URL, idp.Household{Name: "x", Domain: "example.test"}, "token-PLACEHOLDER")
	if err != nil {
		t.Fatal(err)
	}
	lastApplied = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if err := defaultsReady(context.Background(), a); !errors.Is(err, idp.ErrNotReady) {
		t.Fatalf("with a failed blueprint: %v", err)
	}
	if len(applied) != 1 || applied[0] != "/api/v3/managed/blueprints/b-oobe/apply/" {
		t.Errorf("applied again: %q", applied)
	}
	applied = nil
	lastApplied = time.Now().UTC().Format(time.RFC3339Nano)
	_ = defaultsReady(context.Background(), a)
	if len(applied) != 0 {
		t.Errorf("applied again right after it was: %q", applied)
	}
}
