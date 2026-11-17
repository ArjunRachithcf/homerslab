// Package ldaps is the sign-in listener's LDAPS port (ADR 0020, #204): where an app whose sign-in method is LDAP binds,
// at auth.<main domain>:636, from inside its container.
//
// It ends TLS with the household's certificate for the sign-in's name, the edge's, so an app checks the name against
// the roots it already trusts; and it hands the connection on, unread, to the identity provider's LDAP outpost on the
// sign-in network. That keeps the certificate's key a sealed credential of this one unit (ADR 0011), never in the
// provider's database, and the edge a stock Caddy (ADR 0001), which can't carry a TCP stream.
//
// A client must ask for the sign-in's name (SNI): any other name, or none, gets no certificate and no connection. It
// speaks TLS 1.2 or later. Nothing of what passes is read or logged.
package ldaps

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Port is LDAPS's port.
const Port = 636

// Config is the port as its unit runs it.
type Config struct {
	// Name is the sign-in's name, auth.<main domain>: the one name a client may ask for, which the certificate covers.
	Name string
	// CertFile and KeyFile are the household's certificate and its key, as systemd hands the unit its credentials.
	CertFile, KeyFile string
	// Upstream is the LDAP outpost's plain LDAP port, on the sign-in network.
	Upstream string
	// MaxConns is how many connections it carries at once; more are closed at once (default 128).
	MaxConns int
	// PerNetwork is how many of them one app's network may hold (default a quarter of MaxConns): an app's upstream
	// network is a /28 of its own (the agent's), and only that network reaches the port, so one app can't keep the
	// others out however many containers it runs.
	PerNetwork int
	// FirstRequest closes a connection that sends nothing within this long of its handshake (default 30 seconds).
	FirstRequest time.Duration
	// Idle closes a connection that carried nothing either way for this long (default 2 minutes).
	Idle time.Duration
}

// Server carries LDAPS connections to the outpost.
type Server struct {
	cfg  Config
	tls  *tls.Config
	log  *slog.Logger
	sem  chan struct{}
	mu   sync.Mutex
	from map[string]int
	// warned is when the last over-the-limit warning went out; dropped, how many were closed since.
	warned  time.Time
	dropped int
	dial    func(ctx context.Context, network, addr string) (net.Conn, error)
}

// New checks cfg and reads the certificate: one that doesn't cover the sign-in's name is refused.
func New(cfg Config, log *slog.Logger) (*Server, error) {
	cfg.Name = strings.ToLower(strings.TrimSuffix(cfg.Name, "."))
	if !strings.HasPrefix(cfg.Name, "auth.") || strings.ContainsAny(cfg.Name, " */:") {
		return nil, fmt.Errorf("ldaps: %q isn't the sign-in's name", cfg.Name)
	}
	if _, _, err := net.SplitHostPort(cfg.Upstream); err != nil {
		return nil, fmt.Errorf("ldaps: the outpost's address: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("ldaps: the certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("ldaps: the certificate: %w", err)
	}
	if err := leaf.VerifyHostname(cfg.Name); err != nil {
		return nil, fmt.Errorf("ldaps: the certificate doesn't cover %s: %w", cfg.Name, err)
	}
	cfg.MaxConns = max(cfg.MaxConns, 0)
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 128
	}
	if cfg.PerNetwork <= 0 {
		cfg.PerNetwork = max(cfg.MaxConns/4, 1)
	}
	if cfg.FirstRequest <= 0 {
		cfg.FirstRequest = 30 * time.Second
	}
	if cfg.Idle <= 0 {
		cfg.Idle = 2 * time.Minute
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{cfg: cfg, log: log, sem: make(chan struct{}, cfg.MaxConns), from: map[string]int{}, dial: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}
	s.tls = &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if !strings.EqualFold(strings.TrimSuffix(hello.ServerName, "."), cfg.Name) {
				return nil, errors.New("ldaps: no certificate for that name")
			}
			return &cert, nil
		},
	}
	return s, nil
}

// Serve carries every connection ln accepts until ctx ends or ln fails, then waits for those it carries.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); wg.Wait() }()
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return err
		}
		src := network(c.RemoteAddr())
		if !s.take(src) {
			c.Close() // over the limit
			s.refused(src)
			continue
		}
		wg.Add(1)
		go func() {
			defer func() { s.give(src); wg.Done() }()
			s.carry(ctx, c)
		}()
	}
}

// AppNetwork is the size of an app's upstream network, as the agent gives them out: the unit the port counts by.
const AppNetwork = 28

// network is the app network an address is in: its /28 (IPv4), or the address itself.
func network(a net.Addr) string {
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return a.String()
	}
	if ip := ap.Addr().Unmap(); ip.Is4() {
		p, _ := ip.Prefix(AppNetwork)
		return p.String()
	}
	return ap.Addr().String()
}

// refused logs a connection closed over the limit: at most once a minute, with how many since, so a flood of them
// can't flood the journal.
func (s *Server) refused(src string) {
	s.mu.Lock()
	s.dropped++
	now := time.Now()
	if now.Sub(s.warned) < time.Minute {
		s.mu.Unlock()
		return
	}
	n := s.dropped
	s.warned, s.dropped = now, 0
	s.mu.Unlock()
	s.log.Warn("LDAPS connections over the limit, closed", "last", src, "count", n, "max", s.cfg.MaxConns, "perNetwork", s.cfg.PerNetwork)
}

// take holds one of the port's connections for src, unless the port or src is at its limit.
func (s *Server) take(src string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.from[src] >= s.cfg.PerNetwork {
		return false
	}
	select {
	case s.sem <- struct{}{}:
	default:
		return false
	}
	s.from[src]++
	return true
}

func (s *Server) give(src string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	<-s.sem
	if s.from[src]--; s.from[src] <= 0 {
		delete(s.from, src)
	}
}

// carry ends TLS on c and copies it to the outpost and back.
func (s *Server) carry(ctx context.Context, c net.Conn) {
	defer c.Close()
	tc := tls.Server(c, s.tls)
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := tc.HandshakeContext(hctx)
	cancel()
	if err != nil {
		return // a client that asked for another name, or no TLS: nothing to say about it
	}
	// Its first request, within FirstRequest: a connection that holds a slot and says nothing is closed.
	first := make([]byte, 16<<10)
	_ = tc.SetReadDeadline(time.Now().Add(s.cfg.FirstRequest))
	n, err := tc.Read(first)
	if err != nil {
		return
	}
	up, err := s.dial(ctx, "tcp", s.cfg.Upstream)
	if err != nil {
		s.log.Warn("the LDAP outpost can't be reached", "err", err)
		return
	}
	defer up.Close()
	_ = up.SetWriteDeadline(time.Now().Add(s.cfg.Idle))
	if _, err := up.Write(first[:n]); err != nil {
		return
	}
	stop := context.AfterFunc(ctx, func() { tc.Close(); up.Close() })
	defer stop()
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(idle{dst, s.cfg.Idle}, idle{src, s.cfg.Idle})
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(up, tc)
	go pipe(tc, up)
	<-done
	// One side has ended: the other gets as long as it stays busy, never longer than Idle quiet.
	<-done
}

// idle is a connection whose every read and write first moves its deadline Idle ahead.
type idle struct {
	net.Conn
	d time.Duration
}

func (c idle) Read(b []byte) (int, error) {
	_ = c.SetDeadline(time.Now().Add(c.d))
	return c.Conn.Read(b)
}

func (c idle) Write(b []byte) (int, error) {
	_ = c.SetDeadline(time.Now().Add(c.d))
	return c.Conn.Write(b)
}

// Listener is the socket systemd passed (socket activation: LISTEN_PID, LISTEN_FDS, the first at fd 3), or nil when it
// passed none.
func Listener() (net.Listener, error) {
	pid, _ := strconv.Atoi(os.Getenv("LISTEN_PID"))
	n, _ := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if pid != os.Getpid() || n < 1 {
		return nil, nil
	}
	os.Unsetenv("LISTEN_PID")
	os.Unsetenv("LISTEN_FDS")
	os.Unsetenv("LISTEN_FDNAMES")
	f := os.NewFile(3, "ldaps")
	defer f.Close()
	return net.FileListener(f)
}

// Main is `homeward edge ldaps`: the unit's process. It serves the socket systemd passed, or --listen (for tests).
func Main(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("edge ldaps", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg Config
	fs.StringVar(&cfg.Name, "name", "", "the sign-in's name, auth.<main domain>")
	fs.StringVar(&cfg.CertFile, "cert", "", "the certificate chain's file")
	fs.StringVar(&cfg.KeyFile, "key", "", "the key's file")
	fs.StringVar(&cfg.Upstream, "upstream", "", "the LDAP outpost's address")
	listen := fs.String("listen", "", "an address to listen at, when systemd passes no socket")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	s, err := New(cfg, log)
	if err != nil {
		log.Error("the LDAPS port can't start", "err", err)
		return 1
	}
	ln, err := Listener()
	if err == nil && ln == nil {
		if *listen == "" {
			err = errors.New("no socket from systemd, and no --listen")
		} else {
			ln, err = net.Listen("tcp", *listen)
		}
	}
	if err != nil {
		log.Error("the LDAPS port can't listen", "err", err)
		return 1
	}
	log.Info("the LDAPS port carries binds to the outpost", "name", s.cfg.Name, "listen", ln.Addr().String(), "upstream", cfg.Upstream)
	if err := s.Serve(ctx, ln); err != nil {
		log.Error("the LDAPS port stopped", "err", err)
		return 1
	}
	return 0
}
