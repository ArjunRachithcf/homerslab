package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/certs"
	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/manager"
)

func runAgent(ctx context.Context, args []string, env Env) error {
	fs := flags("agent", env)
	socket := fs.String("socket", DefaultAgentSocket, "the agent API's Unix socket")
	group := fs.String("socket-group", DefaultGroup, "the group that may use the socket: the manager's")
	sysroot := fs.String("sysroot", "/", "the system root to read the machine's facts from")
	state := fs.String("state-dir", DefaultAgentState, "the agent's state and the sealed credentials")
	setupPort := fs.Int("setup-port", DefaultSetupPort, "the setup listener's port: the firewall table's one LAN exception until it closes")
	if err := parse(fs, args); err != nil {
		return err
	}
	gid, err := groupID(*group)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(env.Stderr, nil))
	log.Info("agent starting", "socket", *socket, "euid", env.Euid())
	return agent.Serve(ctx, agent.Config{Socket: *socket, SocketGID: gid, Sysroot: os.DirFS(*sysroot), Root: "/",
		StateDir: *state, SetupPort: *setupPort, Log: log})
}

func groupID(name string) (int, error) {
	if gid, err := strconv.Atoi(name); err == nil {
		return gid, nil
	}
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, fmt.Errorf("the socket's group: %w", err)
	}
	return strconv.Atoi(g.Gid)
}

func runManager(ctx context.Context, args []string, env Env) error {
	fs := flags("manager", env)
	db := fs.String("db", DefaultDB, "the manager's database")
	agentSocket := fs.String("agent-socket", DefaultAgentSocket, "the primary's agent")
	control := fs.String("control-socket", DefaultControlSocket, "the control socket, for the CLI on this machine")
	listen := fs.String("listen", DefaultListen, "the setup listener, on this machine and its LAN")
	adminSocket := fs.String("admin-socket", "", "Admin's socket for the edge, when systemd doesn't hand it over (homerslab-admin.socket)")
	ca := fs.String("certificate-authority", "letsencrypt", "the ACME certificate authority: letsencrypt, letsencrypt-staging (rehearsals), or a directory URL")
	caRoots := fs.String("certificate-authority-roots", "", "a PEM file of roots that a test certificate authority's directory is served with")
	resolvers := fs.String("dns-resolvers", "", "public DNS resolvers for the certificate's DNS challenge, host:port, comma-separated (Quad9's and Cloudflare's when empty)")
	if err := parse(fs, args); err != nil {
		return err
	}
	authority, err := certificateAuthority(*ca, *caRoots, *resolvers)
	if err != nil {
		return err
	}
	if env.Euid() == 0 {
		return errors.New("the manager runs unprivileged, as the homerslab user; it reaches the host only through the agent")
	}
	setupLn, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer setupLn.Close()
	controlLn, err := listenPrivate(*control)
	if err != nil {
		return err
	}
	defer controlLn.Close()
	bound := netip.MustParseAddrPort(setupLn.Addr().String())
	lanAddress := linkAddress(bound.Addr(), lan.Current) // the manager caches it
	log := slog.New(slog.NewTextHandler(env.Stderr, nil))
	adminLn, err := adminListener(*adminSocket)
	if err != nil {
		return err
	}
	if adminLn != nil {
		defer adminLn.Close()
	}
	m, err := manager.New(manager.Config{DB: *db, AgentSocket: *agentSocket, SetupPort: int(bound.Port()), Local: loopbackOf(bound.Addr()),
		LANAddress: lanAddress, Authority: authority, Log: log})
	if err != nil {
		return err
	}
	defer m.Close()
	log.Info("manager starting", "setup", setupLn.Addr(), "control", *control, "admin", adminLn != nil, "certificateAuthority", authority.Directory)
	return m.Serve(ctx, setupLn, controlLn, adminLn)
}

// linkAddress is this machine's address for the links, for a setup listener bound to h: h itself unless it is
// loopback, or for an unspecified h, the LAN's best address in the families h answers.
func linkAddress(h netip.Addr, current func() lan.LAN) func() netip.Addr {
	if h.IsUnspecified() {
		return func() netip.Addr { return current().AddressFor(h) }
	}
	return func() netip.Addr {
		if h.IsLoopback() {
			return netip.Addr{}
		}
		return h
	}
}

// loopbackOf is the loopback address a listener bound to addr answers on, or the zero Addr.
func loopbackOf(addr netip.Addr) netip.Addr {
	switch {
	case addr.IsUnspecified(), addr.Unmap().Is4() && addr.IsLoopback():
		return netip.AddrFrom4([4]byte{127, 0, 0, 1})
	case addr.IsLoopback():
		return netip.IPv6Loopback()
	}
	return netip.Addr{}
}

// listenPrivate listens on a Unix socket only this user (and root) can open.
func listenPrivate(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	old := syscall.Umask(0o177)
	defer syscall.Umask(old)
	return net.Listen("unix", path)
}

// certificateAuthority is the ACME authority the manager asks: Let's Encrypt, its staging endpoint for rehearsals and
// CI (which never use up production's limits, #80), or a test CA's directory.
func certificateAuthority(name, rootsFile, resolvers string) (certs.Authority, error) {
	var a certs.Authority
	switch name {
	case "letsencrypt":
		a = certs.LetsEncrypt
	case "letsencrypt-staging":
		a = certs.LetsEncryptStaging
	default:
		u, err := url.Parse(name)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return a, fmt.Errorf("the certificate authority %q: letsencrypt, letsencrypt-staging, or an https:// directory", name)
		}
		a = certs.Authority{Name: u.Host, Directory: name}
	}
	if rootsFile != "" {
		b, err := os.ReadFile(rootsFile)
		if err != nil {
			return a, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			return a, fmt.Errorf("no certificate in %s", rootsFile)
		}
		a.HTTP = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	}
	for _, r := range strings.Split(resolvers, ",") {
		if r = strings.TrimSpace(r); r == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(r); err != nil {
			return a, fmt.Errorf("the DNS resolver %q: host:port", r)
		}
		a.DNS.Servers = append(a.DNS.Servers, r)
	}
	return a, nil
}

// adminListener is Admin's socket: handed over by systemd (homerslab-admin.socket, for the edge's group only), or
// made at path for the edge's group (a group this user is in). Without either, Admin isn't served.
func adminListener(path string) (net.Listener, error) {
	if ln, err := activated("admin"); ln != nil || err != nil {
		return ln, err
	}
	if path == "" {
		return nil, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	old := syscall.Umask(0o117) // 0660: this user and its group
	defer syscall.Umask(old)
	return net.Listen("unix", path)
}

// activated is the socket systemd handed this process under name (sd_listen_fds), or nil.
func activated(name string) (net.Listener, error) {
	if pid, err := strconv.Atoi(os.Getenv("LISTEN_PID")); err != nil || pid != os.Getpid() {
		return nil, nil
	}
	n, err := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if err != nil || n <= 0 {
		return nil, nil
	}
	names := strings.Split(os.Getenv("LISTEN_FDNAMES"), ":")
	for i := range n {
		if i < len(names) && names[i] == name {
			f := os.NewFile(uintptr(3+i), name)
			defer f.Close()
			return net.FileListener(f)
		}
	}
	return nil, nil
}
