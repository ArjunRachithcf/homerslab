// Package packaging holds what the distro packages install beside the binary: the systemd units, the users of the
// manager and the edge, and the edge's boot configuration (edge/boot.json, installed as
// /usr/share/homerslab/edge-boot.json). Its tests check that the units run the manager and the edge unprivileged and
// the agent as root.
package packaging
