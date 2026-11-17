// Package tailscale is what Homeward keeps of Tailscale's client on a node (ADR 0005). The mesh adapter comes later;
// for now the agent keeps tailscaled's control socket for root only.
package tailscale

import "github.com/ArjunRachithcf/homeward/internal/mesh"

// ControlSocket is tailscaled's: the packaged unit (v1.104.4, cmd/tailscaled/tailscaled.service) puts it at
// /run/tailscale/tailscaled.sock, in its RuntimeDirectory of mode 0755, and tailscaled makes the socket 0666
// (safesocket). The drop-in makes that folder root's only (the unit's own RuntimeDirectory, with another mode);
// tailscaled leaves a folder that exists as it is, and the CLI as root finds the socket where it always looks.
var ControlSocket = mesh.ControlSocket{
	Unit:       "tailscaled.service",
	DropInPath: "etc/systemd/system/tailscaled.service.d/10-homeward-socket.conf",
	DropIn: []byte(`# Written by HomersLab's agent, which puts it back if changed. tailscaled's control socket is root's only: it controls
# the node's mesh connection, and tailscaled makes it open to every user (0666), so its folder is one only root may
# enter.
[Service]
RuntimeDirectoryMode=0700
`),
	Dir:    "run/tailscale",
	Socket: "run/tailscale/tailscaled.sock",
}
