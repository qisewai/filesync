//go:build !windows

package zeroconf

import (
	"net"

	"golang.org/x/net/ipv4"
)

func listenIPv4Multicast() (*net.UDPConn, error) {
	return net.ListenUDP("udp4", mdnsWildcardAddrIPv4)
}

func sendIPv4Query(conn *ipv4.PacketConn, ifaces []net.Interface, buf []byte) error {
	var cm ipv4.ControlMessage
	for _, iface := range ifaces {
		cm.IfIndex = iface.Index
		conn.WriteTo(buf, &cm, ipv4Addr)
	}
	return nil
}
