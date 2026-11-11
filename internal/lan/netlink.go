package lan

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"syscall"

	"golang.org/x/sys/unix"
)

// ReadKernel asks the kernel over netlink for this machine's links, addresses and routes: every routing table, with
// each route's own table, so the LAN can keep to the main one (#108).
func ReadKernel() (Kernel, error) {
	var k Kernel
	var err error
	if k.Links, err = readLinks(); err != nil {
		return Kernel{}, err
	}
	if k.Addrs, err = readAddrs(); err != nil {
		return Kernel{}, err
	}
	if k.Routes, err = readRoutes(); err != nil {
		return Kernel{}, err
	}
	return k, nil
}

func dump(kind int) ([]syscall.NetlinkMessage, error) {
	b, err := syscall.NetlinkRIB(kind, syscall.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("netlink: %w", err)
	}
	msgs, err := syscall.ParseNetlinkMessage(b)
	if err != nil {
		return nil, fmt.Errorf("netlink: %w", err)
	}
	return msgs, nil
}

func readLinks() ([]Link, error) {
	msgs, err := dump(syscall.RTM_GETLINK)
	if err != nil {
		return nil, err
	}
	var out []Link
	for _, m := range msgs {
		if m.Header.Type != syscall.RTM_NEWLINK || len(m.Data) < syscall.SizeofIfInfomsg {
			continue
		}
		// ifinfomsg: family, padding, type, then the index and the flags.
		index, flags := int32(binary.NativeEndian.Uint32(m.Data[4:8])), binary.NativeEndian.Uint32(m.Data[8:12])
		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			continue
		}
		l := Link{Index: int(index), PointToPoint: flags&syscall.IFF_POINTOPOINT != 0, Loopback: flags&syscall.IFF_LOOPBACK != 0}
		for _, a := range attrs {
			switch a.Attr.Type {
			case syscall.IFLA_IFNAME:
				l.Name = cString(a.Value)
			case unix.IFLA_LINKINFO:
				for _, n := range nested(a.Value) {
					if n.typ == unix.IFLA_INFO_KIND {
						l.Kind = cString(n.value)
					}
				}
			}
		}
		out = append(out, l)
	}
	return out, nil
}

func readAddrs() ([]Addr, error) {
	msgs, err := dump(syscall.RTM_GETADDR)
	if err != nil {
		return nil, err
	}
	var out []Addr
	for _, m := range msgs {
		if m.Header.Type != syscall.RTM_NEWADDR || len(m.Data) < syscall.SizeofIfAddrmsg {
			continue
		}
		// ifaddrmsg: family, prefix length, flags, scope, then the interface's index.
		plen, index := int(m.Data[1]), int(binary.NativeEndian.Uint32(m.Data[4:8]))
		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			continue
		}
		var address, local netip.Addr
		flags := uint32(m.Data[2])
		for _, a := range attrs {
			switch a.Attr.Type {
			case syscall.IFA_ADDRESS:
				address, _ = netip.AddrFromSlice(a.Value)
			case syscall.IFA_LOCAL:
				local, _ = netip.AddrFromSlice(a.Value)
			case unix.IFA_FLAGS:
				if len(a.Value) == 4 {
					flags = binary.NativeEndian.Uint32(a.Value)
				}
			}
		}
		if local.IsValid() { // the machine's own end; IFA_ADDRESS is the peer's on a point-to-point link
			address = local
		}
		if !address.IsValid() || plen > address.BitLen() {
			continue
		}
		out = append(out, Addr{Index: index, Prefix: netip.PrefixFrom(address, plen), Flags: flags})
	}
	return out, nil
}

func readRoutes() ([]Route, error) {
	msgs, err := dump(syscall.RTM_GETROUTE)
	if err != nil {
		return nil, err
	}
	var out []Route
	for _, m := range msgs {
		if m.Header.Type != syscall.RTM_NEWROUTE || len(m.Data) < syscall.SizeofRtMsg {
			continue
		}
		// rtmsg: family, destination length, source length, TOS, table, protocol, scope, type, flags.
		family, dstLen, table, typ := m.Data[0], int(m.Data[1]), m.Data[4], m.Data[7]
		if family != syscall.AF_INET && family != syscall.AF_INET6 { // not the multicast or MPLS tables
			continue
		}
		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			continue
		}
		r := Route{Table: uint32(table), Type: typ}
		dst := netip.IPv4Unspecified()
		if family == syscall.AF_INET6 {
			dst = netip.IPv6Unspecified()
		}
		var hops []Route // a multipath route's next hops, each its own route here
		for _, a := range attrs {
			switch a.Attr.Type {
			case syscall.RTA_DST:
				dst, _ = netip.AddrFromSlice(a.Value)
			case syscall.RTA_GATEWAY, unix.RTA_VIA:
				r.Gateway = true
			case syscall.RTA_OIF:
				if len(a.Value) == 4 {
					r.Index = int(binary.NativeEndian.Uint32(a.Value))
				}
			case unix.RTA_TABLE: // the table's full number: rtm_table holds only up to 255
				if len(a.Value) == 4 {
					r.Table = binary.NativeEndian.Uint32(a.Value)
				}
			case unix.RTA_MULTIPATH:
				hops = nextHops(a.Value)
			}
		}
		if !dst.IsValid() || dstLen > dst.BitLen() {
			continue
		}
		r.Dst = netip.PrefixFrom(dst, dstLen)
		if hops == nil {
			out = append(out, r)
			continue
		}
		for _, h := range hops {
			h.Table, h.Type, h.Dst = r.Table, r.Type, r.Dst
			out = append(out, h)
		}
	}
	return out, nil
}

// nextHops reads RTA_MULTIPATH: rtnexthop structs (length, flags, hops, the interface's index), each followed by
// its own attributes.
func nextHops(b []byte) []Route {
	var out []Route
	for len(b) >= unix.SizeofRtNexthop {
		n := int(binary.NativeEndian.Uint16(b[0:2]))
		if n < unix.SizeofRtNexthop || n > len(b) {
			break
		}
		h := Route{Index: int(int32(binary.NativeEndian.Uint32(b[4:8])))}
		for _, a := range attrList(b[unix.SizeofRtNexthop:n]) {
			if a.typ == syscall.RTA_GATEWAY || a.typ == unix.RTA_VIA {
				h.Gateway = true
			}
		}
		out = append(out, h)
		if align(n) >= len(b) {
			break
		}
		b = b[align(n):]
	}
	return out
}

type attr struct {
	typ   uint16
	value []byte
}

// nested reads the attributes inside one: as attrList, with the nested flag cleared.
func nested(b []byte) []attr {
	list := attrList(b)
	for i := range list {
		list[i].typ &^= syscall.NLA_F_NESTED
	}
	return list
}

// attrList reads a run of netlink attributes: length, type, value, each padded to four bytes.
func attrList(b []byte) []attr {
	var out []attr
	for len(b) >= syscall.SizeofRtAttr {
		n := int(binary.NativeEndian.Uint16(b[0:2]))
		if n < syscall.SizeofRtAttr || n > len(b) {
			break
		}
		out = append(out, attr{typ: binary.NativeEndian.Uint16(b[2:4]), value: b[syscall.SizeofRtAttr:n]})
		if align(n) >= len(b) {
			break
		}
		b = b[align(n):]
	}
	return out
}

func align(n int) int { return (n + syscall.NLMSG_ALIGNTO - 1) &^ (syscall.NLMSG_ALIGNTO - 1) }

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
