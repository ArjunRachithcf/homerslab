package netbird_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/mesh/netbird"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// A token goes only to the address the admin gave: a redirect elsewhere is an answer, never followed.
func TestATokenIsNeverSentWhereARedirectPoints(t *testing.T) {
	testenv.Isolate(t)
	var reached atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		_, _ = w.Write([]byte(`{"setup_required":true}`))
	}))
	defer elsewhere.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	m, err := netbird.Open(redirect.URL, mesh.Token{Value: "nbp_SECRET", UserID: "u", ID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Report(context.Background()); err == nil {
		t.Error("a redirect was taken for an answer")
	}
	if _, err := netbird.Adopt(context.Background(), redirect.URL, "nbp_SECRET"); err == nil {
		t.Error("adopting through a redirect worked")
	}
	if _, err := netbird.Probe(context.Background(), redirect.URL); err == nil {
		t.Error("probing through a redirect worked")
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the redirect's target got %d requests", n)
	}
}
