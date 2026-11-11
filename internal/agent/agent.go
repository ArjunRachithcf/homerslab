// Package agent is the agent's API: the process on every node that owns its host and runs as root. So far it reports
// the node's facts, and reads apps' compose projects for planning.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/engine"
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
	reads := newReads()
	mux.HandleFunc("POST /v1/projects/read", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Project string `json:"project"`
			Compose string `json:"compose"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not-json"})
			return
		}
		p, err := reads.read(r.Context(), req.Project, []byte(req.Compose))
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, p)
		case errors.Is(err, errBusy):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "busy"})
		case errors.Is(err, errReadTimeout):
			log.Warn("a compose read ran out of time", "project", req.Project)
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "read-timeout"})
		case errors.Is(err, errNoReader):
			log.Error("starting a compose read", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no-reader"})
		case errors.Is(err, compose.ErrNotOurs):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not-ours"})
		case errors.Is(err, compose.ErrNotPlain):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "not-plain", "detail": err.Error()})
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

// ReadDeadline is how long a compose read may take before the agent stops it. The manager waits longer, so that it
// hears the agent's read-timeout.
const ReadDeadline = 10 * time.Second

var readDeadline = ReadDeadline

// readCommand is how the agent starts a compose read: itself, in its read-project mode.
var readCommand = func() ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return []string{exe, "agent", "read-project"}, nil
}

var (
	errBusy        = errors.New("another compose read is running")
	errReadTimeout = errors.New("the compose read ran out of time")
	errNoReader    = errors.New("the compose reader didn't start")
)

// reads runs compose reads one at a time, each in a child process (ReadProjectMain) that the agent kills at the
// deadline: the deadline really stops the work, and the slot is free again at once. The plain-file rules
// (engine/compose) bound how long a read can take; the child's own limits and the agent's unit bound the rest.
type reads struct{ slot chan struct{} }

func newReads() *reads { return &reads{slot: make(chan struct{}, 1)} }

func (rs *reads) read(ctx context.Context, name string, file []byte) (engine.Project, error) {
	ctx, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	select {
	case rs.slot <- struct{}{}:
	case <-ctx.Done():
		return engine.Project{}, errBusy
	}
	defer func() { <-rs.slot }()
	argv, err := readCommand()
	if err != nil {
		return engine.Project{}, fmt.Errorf("%w: %w", errNoReader, err)
	}
	// The child's own folder: empty, and the only one it may write to.
	dir, err := os.MkdirTemp("", "homerslab-read-")
	if err != nil {
		return engine.Project{}, fmt.Errorf("%w: %w", errNoReader, err)
	}
	defer os.RemoveAll(dir)
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, nobody, nobody); err != nil {
			return engine.Project{}, fmt.Errorf("%w: %w", errNoReader, err)
		}
	}
	seconds := int(readDeadline/time.Second) + 1
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], "--cpu-seconds", strconv.Itoa(seconds), name)...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "TMPDIR=" + dir, "DOCKER_CONFIG=" + filepath.Join(dir, ".docker"), "PATH=/usr/bin:/bin"}
	cmd.Stdin = bytes.NewReader(file)
	var out, errOut capped
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return engine.Project{}, errReadTimeout
	}
	var res readResult
	if err := json.Unmarshal(out.b.Bytes(), &res); err != nil {
		return engine.Project{}, fmt.Errorf("%w: %v (%v) %s", errNoReader, err, runErr, strings.TrimSpace(errOut.b.String()))
	}
	switch res.Error {
	case "":
		return *res.Project, nil
	case "not-ours":
		return engine.Project{}, compose.ErrNotOurs
	case "not-plain":
		return engine.Project{}, fmt.Errorf("%w (%s)", compose.ErrNotPlain, res.Detail)
	default:
		return engine.Project{}, errors.New(res.Detail)
	}
}

// capped is a child's output, of which it keeps the first MiB.
type capped struct{ b bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := 1<<20 - c.b.Len(); room > 0 {
		c.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
