//go:build !linux

package lan

import "errors"

// ReadKernel needs Linux's netlink: elsewhere there is no LAN.
func ReadKernel() (Kernel, error) {
	return Kernel{}, errors.New("the LAN is read over netlink, on Linux only")
}
