package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/user"
	"strconv"
	"syscall"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/manager"
	"github.com/ArjunRachithcf/homerslab/internal/node"
)

func runAgent(ctx context.Context, args []string, env Env) error {
	fs := flags("agent", env)
	socket := fs.String("socket", DefaultAgentSocket, "the agent API's Unix socket")
	group := fs.String("socket-group", DefaultGroup, "the group that may use the socket: the manager's")
	sysroot := fs.String("sysroot", "/", "the system root to read the machine's facts from")
	if err := parse(fs, args); err != nil {
		return err
	}
	gid, err := groupID(*group)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(env.Stderr, nil))
	log.Info("agent starting", "socket", *socket, "euid", env.Euid())
	return agent.Serve(ctx, agent.Config{Socket: *socket, SocketGID: gid, Sysroot: node.Dir(*sysroot), Log: log})
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
	adminListen := fs.String("admin-listen", DefaultAdminListen, "Admin's listener, on this machine's loopback")
	if err := parse(fs, args); err != nil {
		return err
	}
	adminAt, err := netip.ParseAddrPort(*adminListen)
	if err != nil || !adminAt.Addr().IsLoopback() {
		return fmt.Errorf("--admin-listen %s: Admin answers on loopback only (127.0.0.1:<port> or [::1]:<port>) until it signs people in", *adminListen)
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
	adminLn, err := net.Listen("tcp", adminAt.String())
	if err != nil {
		return err
	}
	defer adminLn.Close()
	bound := netip.MustParseAddrPort(setupLn.Addr().String())
	lanAddress := func() netip.Addr { return lan.Current().Address() } // the manager caches it
	if h := bound.Addr(); !h.IsUnspecified() {
		lanAddress = func() netip.Addr { // listening on one address: the links use it, unless it's loopback
			if h.IsLoopback() {
				return netip.Addr{}
			}
			return h
		}
	}
	log := slog.New(slog.NewTextHandler(env.Stderr, nil))
	m, err := manager.New(manager.Config{DB: *db, AgentSocket: *agentSocket, SetupPort: int(bound.Port()), Local: loopbackOf(bound.Addr()),
		AdminPort: int(netip.MustParseAddrPort(adminLn.Addr().String()).Port()), LANAddress: lanAddress, Log: log})
	if err != nil {
		return err
	}
	defer m.Close()
	log.Info("manager starting", "setup", setupLn.Addr(), "control", *control, "admin", adminLn.Addr())
	return m.Serve(ctx, setupLn, controlLn, adminLn)
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
