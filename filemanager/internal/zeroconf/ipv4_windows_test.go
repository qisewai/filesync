package zeroconf

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
)

func TestWindowsBrowseOpensMulticastSocket(t *testing.T) {
	if len(listMulticastInterfaces()) == 0 {
		t.Skip("no active multicast interface")
	}
	r, err := NewResolver(SelectIPTraffic(IPv4))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	entries := make(chan *ServiceEntry, 32)
	if err := r.Browse(ctx, "_filesync-test._tcp", "local.", entries); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	for range entries {
	}
}

func TestSendIPv4QueryUsesWorkingInterface(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var iface *net.Interface
	for i := range ifaces {
		if ifaces[i].Flags&net.FlagUp == 0 || ifaces[i].Flags&net.FlagMulticast == 0 || ifaces[i].Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifaces[i].Addrs()
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil && ip.To4() != nil && !ip.IsLinkLocalUnicast() {
				iface = &ifaces[i]
				break
			}
		}
		if iface != nil {
			break
		}
	}
	if iface == nil {
		t.Skip("no active multicast IPv4 interface")
	}
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Skipf("multicast unavailable: %v", err)
	}
	defer listener.Close()
	if err := ipv4.NewPacketConn(listener).JoinGroup(iface, &net.UDPAddr{IP: mdnsGroupIPv4}); err != nil {
		t.Skipf("multicast unavailable: %v", err)
	}
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	oldAddr := ipv4Addr
	ipv4Addr = listener.LocalAddr().(*net.UDPAddr)
	ipv4Addr.IP = mdnsGroupIPv4
	defer func() { ipv4Addr = oldAddr }()
	packet := []byte("interface-test")
	packetSender := ipv4.NewPacketConn(sender)
	if err := packetSender.SetMulticastLoopback(true); err != nil {
		t.Fatal(err)
	}
	if err := sendIPv4Query(packetSender, []net.Interface{{Index: -1}, *iface}, packet); err != nil {
		t.Fatal(err)
	}
	listener.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 128)
	n, _, err := listener.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != string(packet) {
		t.Fatalf("multicast query not received on %s: %q, %v", iface.Name, buf[:n], err)
	}
}
