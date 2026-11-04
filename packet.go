package main

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

func parsePacket(b []byte) (Event, error) {
	var e Event
	var proto byte
	var p int
	if len(b) < 20 {
		return e, fmt.Errorf("short IP packet")
	}
	switch b[0] >> 4 {
	case 4:
		p = int(b[0]&15) * 4
		if p < 20 || p > len(b) {
			return e, fmt.Errorf("bad IPv4 header")
		}
		e.Family = 4
		e.Src = netip.AddrFrom4([4]byte(b[12:16])).String()
		e.Dst = netip.AddrFrom4([4]byte(b[16:20])).String()
		e.Bytes = int64(binary.BigEndian.Uint16(b[2:4]))
		proto = b[9]
		if binary.BigEndian.Uint16(b[6:8])&0x1fff != 0 {
			p = len(b)
		}
	case 6:
		if len(b) < 40 {
			return e, fmt.Errorf("short IPv6")
		}
		e.Family = 6
		e.Src = netip.AddrFrom16([16]byte(b[8:24])).String()
		e.Dst = netip.AddrFrom16([16]byte(b[24:40])).String()
		e.Bytes = int64(binary.BigEndian.Uint16(b[4:6])) + 40
		proto = b[6]
		p = 40
		for i := 0; i < 8; i++ {
			if proto == 0 || proto == 43 || proto == 60 {
				if p+2 > len(b) {
					break
				}
				n := (int(b[p+1]) + 1) * 8
				proto = b[p]
				p += n
				if p > len(b) {
					break
				}
			} else if proto == 44 {
				if p+8 > len(b) {
					break
				}
				next := b[p]
				offset := binary.BigEndian.Uint16(b[p+2:p+4]) & 0xfff8
				p += 8
				proto = next
				if offset != 0 {
					p = len(b)
					break
				}
			} else {
				break
			}
		}
	default:
		return e, fmt.Errorf("unsupported IP version")
	}
	switch proto {
	case 6:
		e.Proto = "TCP"
	case 17:
		e.Proto = "UDP"
	case 1:
		e.Proto = "ICMP"
	case 58:
		e.Proto = "ICMPv6"
	default:
		e.Proto = fmt.Sprintf("IP/%d", proto)
	}
	if (proto == 6 || proto == 17) && p+4 <= len(b) {
		e.SPort = int(binary.BigEndian.Uint16(b[p : p+2]))
		e.DPort = int(binary.BigEndian.Uint16(b[p+2 : p+4]))
	}
	e.Reason = "已知目标直连转发被阻断"
	e.Source = "nflog"
	return e, nil
}
