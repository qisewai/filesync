package zeroconf

import (
	"context"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/net/ipv4"
	"golang.org/x/sys/windows"
)

// Windows cannot bind to 224.0.0.0. Allow other mDNS listeners to share 5353.
func listenIPv4Multicast() (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var sockErr error
		if err := raw.Control(func(fd uintptr) {
			sockErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
		}); err != nil {
			return err
		}
		return sockErr
	}}
	c, err := lc.ListenPacket(context.Background(), "udp4", ":5353")
	if err != nil {
		return nil, err
	}
	return c.(*net.UDPConn), nil
}

// Windows ignores ipv4.ControlMessage.IfIndex in WriteTo. Set the socket's
// multicast interface before each send instead. Queries are serialized by the
// resolver: its periodic sender starts after the initial query returns.
func sendIPv4Query(conn *ipv4.PacketConn, ifaces []net.Interface, buf []byte) error {
	var lastErr error
	sent := 0
	for i := range ifaces {
		if err := conn.SetMulticastInterface(&ifaces[i]); err != nil {
			lastErr = err
			continue
		}
		if _, err := conn.WriteTo(buf, nil, ipv4Addr); err != nil {
			lastErr = err
			continue
		}
		sent++
	}
	if sent == 0 {
		return fmt.Errorf("no multicast interface could send the discovery query: %v", lastErr)
	}
	return nil
}
