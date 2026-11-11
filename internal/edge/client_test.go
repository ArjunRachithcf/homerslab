package edge_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/edge"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// fakeAdmin answers like Caddy's admin API: /load takes a configuration, /config/ gives it back re-marshalled.
type fakeAdmin struct {
	mu     sync.Mutex
	config []byte
}

func (f *fakeAdmin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/load":
		b, _ := io.ReadAll(r.Body)
		if !json.Valid(b) {
			http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
			return
		}
		f.config = b
	case r.Method == http.MethodGet && r.URL.Path == "/config/":
		var v any
		_ = json.Unmarshal(f.config, &v)
		b, _ := json.Marshal(v) // compact, keys sorted: not what was loaded, byte for byte
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

func TestTheAgentLoadsTheEdgeAndSeesWhetherItRunsIt(t *testing.T) {
	testenv.Isolate(t)
	sock := filepath.Join(t.TempDir(), "admin.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: &fakeAdmin{config: []byte(`{"admin":{"listen":"boot"}}`)}}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	ctx := context.Background()
	c := edge.NewClient(sock)
	want, err := edge.Render(thePrimary())
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := c.Running(ctx, want); err != nil || ok {
		t.Fatalf("a fresh edge runs the configuration already: %v %v", ok, err)
	}
	if err := c.Load(ctx, want); err != nil {
		t.Fatal(err)
	}
	if ok, err := c.Running(ctx, want); err != nil || !ok {
		t.Fatalf("after the load: running %v, %v", ok, err)
	}
	if err := c.Load(ctx, []byte("not json")); err == nil {
		t.Error("a configuration the edge refused came back as loaded")
	}
}
