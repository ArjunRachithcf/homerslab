package mesh

// ControlSocket is a mesh client's control socket on a node, as the agent keeps it: for root only. Whoever can connect
// to it controls the node's mesh connection (up, down, its control server), and both NetBird's daemon and tailscaled
// make it 0666 (#200). Their own option can't make it root's: the agent puts it in a folder only root may enter, with
// a systemd drop-in for the daemon's unit, and corrects that folder's mode when it drifts. Paths are relative to the
// machine's root.
type ControlSocket struct {
	// Unit is the daemon's systemd unit.
	Unit string
	// DropInPath and DropIn are the agent's drop-in for Unit: the folder made 0700, root's, at every start.
	DropInPath string
	DropIn     []byte
	// Dir is the socket's folder, which only root may enter, and Socket the socket in it.
	Dir    string
	Socket string
	// Link, when set, is where the client's CLI looks for the socket: a link to the socket in Dir, which the drop-in
	// makes at every start. A socket there instead is the daemon running without the drop-in.
	Link string
}
