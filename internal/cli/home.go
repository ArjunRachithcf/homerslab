package cli

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/home"
	"github.com/ArjunRachithcf/homerslab/internal/surfaces"
)

// homeSecret is Home's client secret, as systemd hands it to Home's unit: a credential in its credentials directory.
const homeSecret = "manager-home-client-secret"

// runHome is Home's process (#21): unprivileged, from homerslab-home.service, answering the edge on Home's socket.
func runHome(ctx context.Context, args []string, env Env) error {
	fs := flags("home", env)
	projection := fs.String("projection", DefaultProjection, "the projection the manager keeps for Home")
	secret := fs.String("client-secret", filepath.Join(os.Getenv("CREDENTIALS_DIRECTORY"), homeSecret),
		"the file of Home's client secret at the sign-in (its unit's credential)")
	socket := fs.String("socket", "", "Home's socket for the edge, when systemd doesn't hand it over (homerslab-home.socket)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if env.Euid() == 0 {
		return errors.New("Home runs unprivileged, as the homerslab-home user; it reads only the projection")
	}
	ln, err := activated("home")
	if err == nil && ln == nil {
		if *socket == "" {
			return errors.New("no socket: systemd hands Home its socket (homerslab-home.socket), or --socket names one")
		}
		if err := os.Remove(*socket); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		old := syscall.Umask(0o117) // 0660: this user and its group
		ln, err = net.Listen("unix", *socket)
		syscall.Umask(old)
	}
	if err != nil {
		return err
	}
	defer ln.Close()
	log := slog.New(slog.NewTextHandler(env.Stderr, nil))
	h := home.New(home.Config{Projection: *projection, ClientSecret: *secret, Web: surfaces.Dist(), Log: log})
	srv := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("Home starting", "projection", *projection)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
