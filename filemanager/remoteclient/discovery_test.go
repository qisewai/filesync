package fileindex

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"filemanager/internal/zeroconf"
	"github.com/miekg/dns"
)

func TestDiscoveryEntry(t *testing.T) {
	valid := func() *zeroconf.ServiceEntry {
		return &zeroconf.ServiceEntry{Port: 18090, TTL: 120, Text: []string{
			"txtvers=1", "app=fileindex", "id=0123456789abcdef0123456789abcdef", "name=家庭 NAS", "scheme=http", "path=/",
		}, AddrIPv4: []net.IP{net.ParseIP("192.168.1.8")}}
	}
	s, ok := discoveryEntry(valid())
	if !ok || s.Addr != "192.168.1.8:18090" || s.Name != "家庭 NAS" {
		t.Fatalf("%+v %v", s, ok)
	}
	for _, change := range []func(*zeroconf.ServiceEntry){
		func(e *zeroconf.ServiceEntry) { e.Port = 0 },
		func(e *zeroconf.ServiceEntry) { e.Port = 65536 },
		func(e *zeroconf.ServiceEntry) { e.TTL = 0 },
		func(e *zeroconf.ServiceEntry) { e.AddrIPv4 = []net.IP{net.ParseIP("127.0.0.1")} },
		func(e *zeroconf.ServiceEntry) { e.Text[0] = "txtvers=2" },
		func(e *zeroconf.ServiceEntry) { e.Text[1] = "app=other" },
		func(e *zeroconf.ServiceEntry) { e.Text[2] = "id=invalid" },
		func(e *zeroconf.ServiceEntry) { e.Text[3] = "name=bad/name" },
		func(e *zeroconf.ServiceEntry) { e.Text[4] = "scheme=https" },
		func(e *zeroconf.ServiceEntry) { e.Text[5] = "path=/other" },
		func(e *zeroconf.ServiceEntry) { e.Text = append(e.Text, "scheme=http") },
	} {
		e := valid()
		change(e)
		if s, ok := discoveryEntry(e); ok {
			t.Fatalf("accepted %+v as %+v", e, s)
		}
	}
}

func TestDiscoveryCancelledBeforeOpeningSockets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Discover(ctx, func([]DiscoveredServer) { t.Fatal("callback after cancellation") }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDiscoveryTXTUnicodeWireRoundTrip(t *testing.T) {
	name := `家庭 NAS \ "一号"`
	m := &dns.Msg{Answer: []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: "test.local.", Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 120}, Txt: []string{"name=" + strings.ReplaceAll(name, `\`, `\\`)}}}}
	packet, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	var decoded dns.Msg
	if err := decoded.Unpack(packet); err != nil {
		t.Fatal(err)
	}
	text, ok := decodeDiscoveryTXT(decoded.Answer[0].(*dns.TXT).Txt[0])
	if !ok || text != "name="+name {
		t.Fatalf("%q != %q", text, name)
	}
}
