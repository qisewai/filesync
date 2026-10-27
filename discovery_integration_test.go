package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"filesync/config"
	"filesync/fileindex"
)

// Explicitly opt in to a real multicast test, against two disposable production
// server processes. It never contacts the NAS or writes a user's configuration.
func TestLANDiscoveryWithProductionServers(t *testing.T) {
	binary := os.Getenv("FILEINDEX_DISCOVERY_BIN")
	if binary == "" {
		t.Skip("set FILEINDEX_DISCOVERY_BIN to opt in to multicast integration")
	}
	ifaceName := os.Getenv("FILEINDEX_DISCOVERY_IFACE")
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		t.Fatal("provide FILEINDEX_DISCOVERY_IFACE: ", err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	var ip net.IP
	for _, addr := range addrs {
		if v, _, e := net.ParseCIDR(addr.String()); e == nil && v.To4() != nil {
			ip = v
			break
		}
	}
	if ip == nil {
		t.Fatal("no IPv4 address on interface")
	}
	start := func(name string) (string, func()) {
		dir := t.TempDir()
		data := filepath.Join(dir, "data")
		conf := filepath.Join(dir, "config")
		if err := os.Mkdir(data, 0700); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
		if err != nil {
			t.Fatal(err)
		}
		addr := listener.Addr().String()
		listener.Close()
		log, err := os.Create(filepath.Join(dir, "server.log"))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary)
		cmd.Env = append(os.Environ(), "FI_DATA="+data, "FI_CONFIG="+conf, "FI_ADDR="+addr, "FI_NAME="+name, "FI_DISCOVERY=1", "FI_DISCOVERY_IFACES="+ifaceName, "FI_GOTENBERG_URL=")
		cmd.Stdout = log
		cmd.Stderr = log
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		stop := func() { once.Do(func() { cmd.Process.Signal(syscall.SIGTERM); cmd.Wait(); log.Close() }) }
		t.Cleanup(stop)
		httpClient := &http.Client{Timeout: time.Second, Transport: &http.Transport{}}
		defer httpClient.CloseIdleConnections()
		deadline := time.Now().Add(10 * time.Second)
		for {
			resp, e := httpClient.Get("http://" + addr + "/api/auth/status")
			if e == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					break
				}
			}
			if time.Now().After(deadline) {
				b, _ := os.ReadFile(filepath.Join(dir, "server.log"))
				t.Fatalf("server failed: %v %s", e, b)
			}
			time.Sleep(100 * time.Millisecond)
		}
		return addr, stop
	}
	nameA := "发现验证 NAS A"
	nameB := "发现验证 NAS B"
	addrA, stopA := start(nameA)
	addrB, _ := start(nameB)
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.discover = fileindex.Discover
	h.a.openServers()
	h.settle()
	for _, expected := range []struct{ name, addr string }{{nameA, addrA}, {nameB, addrB}} {
		found := false
		for _, s := range h.a.lan.servers {
			if s.Name == expected.name && s.Addr == expected.addr {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s %s in %+v; error %s", expected.name, expected.addr, h.a.lan.servers, h.a.lan.err)
		}
	}
	matching := func() []fileindex.DiscoveredServer {
		var rows []fileindex.DiscoveredServer
		for _, s := range h.a.lan.servers {
			if s.Name == nameA || s.Name == nameB {
				rows = append(rows, s)
			}
		}
		return rows
	}
	if len(matching()) != 2 {
		t.Fatalf("unexpected duplicates: %+v", h.a.lan.servers)
	}
	if out := os.Getenv("FILESYNC_DISCOVERY_REAL_SNAPSHOT"); out != "" {
		saveResumeSnapshot(t, h, out)
	}
	h.click("选择发现的服务器 " + nameA + " " + addrA)
	if h.a.nas.name != nameA || h.a.nas.addr != addrA {
		t.Fatal("actual discovery did not fill form")
	}
	stopA()
	h.a.startDiscovery()
	h.settle()
	for _, s := range h.a.lan.servers {
		if s.Name == nameA {
			t.Fatal("offline server remained after refresh")
		}
	}
	if rows := matching(); len(rows) != 1 || rows[0].Addr != addrB {
		t.Fatalf("online server missing after refresh: %+v", h.a.lan.servers)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fileindex.Discover(ctx, func([]fileindex.DiscoveredServer) { t.Fatal("late update") }); err == nil {
		t.Fatal("canceled browse succeeded")
	}
}

// Read-only opt-in check against a deployed server; never signs in or writes it.
func TestLANDiscoveryExistingServer(t *testing.T) {
	addr := os.Getenv("FILEINDEX_DISCOVERY_EXPECT_ADDR")
	if addr == "" {
		t.Skip("set FILEINDEX_DISCOVERY_EXPECT_ADDR for a deployed-server check")
	}
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.discover = fileindex.Discover
	h.a.openServers()
	h.settle()
	var selected *fileindex.DiscoveredServer
	for _, s := range h.a.lan.servers {
		if s.Addr == addr {
			s := s
			selected = &s
			break
		}
	}
	if selected == nil {
		t.Fatalf("%s was not discovered: %+v (%s)", addr, h.a.lan.servers, h.a.lan.err)
	}
	if out := os.Getenv("FILESYNC_DISCOVERY_NAS_SNAPSHOT"); out != "" {
		saveResumeSnapshot(t, h, out)
	}
	h.click("选择发现的服务器 " + selected.Name + " " + addr)
	if h.a.nas.addr != addr || h.a.nas.name != selected.Name {
		t.Fatal("NAS selection did not populate connection form")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := fileindex.New(addr, "").Authed(ctx); err != nil {
		t.Fatal("discovered endpoint was unreachable: ", err)
	}
	t.Logf("discovered %s at %s and verified HTTP without signing in", selected.Name, addr)
}
