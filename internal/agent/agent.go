// Package agent is the agent's API: the process on every node that owns its host and runs as root. So far it reports
// the node's facts, and reads apps' compose projects for planning.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/engine/compose"
	"github.com/ArjunRachithcf/homerslab/internal/node"
)

// Sysroot is the system root the agent reads its facts from: "/" on a real machine.
type Sysroot = fs.FS

// Config is where the agent listens and what it reads.
type Config struct {
	// Socket is the Unix socket of the agent's API. Only root and SocketGID may connect to it.
	Socket string
	// SocketGID is the manager's group.
	SocketGID int
	Sysroot   Sysroot
	Log       *slog.Logger
}

// Handler serves the agent's API.
func Handler(sysroot Sysroot, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/node", func(w http.ResponseWriter, _ *http.Request) {
		facts, err := node.Read(sysroot)
		if err != nil {
			log.Error("reading the node's facts", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "facts-unreadable"})
			return
		}
		writeJSON(w, http.StatusOK, facts)
	})
	// The reader is made once it works: a failure to make it is tried again on the next request.
	var mu sync.Mutex
	var made *compose.Reader
	reader := func() (*compose.Reader, error) {
		mu.Lock()
		defer mu.Unlock()
		if made != nil {
			return made, nil
		}
		r, err := compose.NewReader("")
		if err == nil {
			made = r
		}
		return r, err
	}
	mux.HandleFunc("POST /v1/projects/read", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Project string `json:"project"`
			Compose string `json:"compose"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not-json"})
			return
		}
		rd, err := reader()
		if err != nil {
			log.Error("making the compose reader", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no-reader"})
			return
		}
		p, err := rd.Read(r.Context(), req.Project, []byte(req.Compose))
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, p)
		case errors.Is(err, compose.ErrNotOurs):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not-ours"})
		case errors.Is(err, compose.ErrNotPlain):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "not-plain"})
		default:
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid-compose", "detail": err.Error()})
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not-found"})
	})
	return mux
}

// Serve runs the agent's API on its socket until ctx ends.
func Serve(ctx context.Context, c Config) error {
	ln, err := listenUnix(c.Socket, c.SocketGID, 0o660)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: Handler(c.Sysroot, c.Log), ReadHeaderTimeout: 10 * time.Second}
	return serveUntil(ctx, srv, ln)
}

// listenUnix listens on a fresh socket at path, owned by this process's user and gid, with mode perm.
func listenUnix(path string, gid int, perm os.FileMode) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("removing the old socket: %w", err)
	}
	// Born owner-only (connecting needs write), so nobody else can connect before it has its group and mode.
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chown(path, -1, gid); err != nil {
		ln.Close()
		return nil, fmt.Errorf("giving the socket to group %d: %w", gid, err)
	}
	if err := os.Chmod(path, perm); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func serveUntil(ctx context.Context, srv *http.Server, ln net.Listener) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			return err
		}
		if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
