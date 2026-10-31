package fileindex

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"filemanager/internal/zeroconf"
)

const DiscoveryService = "_fileindex._tcp"

// DiscoveredServer is untrusted public metadata. Selecting one still requires
// the normal login; discovery must never carry or reassign an existing token.
type DiscoveredServer struct{ ID, Name, Addr string }

// miekg/dns represents TXT bytes using DNS presentation escapes (\DDD),
// including each UTF-8 byte. Decode before validating/displaying metadata.
func decodeDiscoveryTXT(s string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			return "", false
		}
		if i+2 < len(s) && s[i] >= '0' && s[i] <= '9' && s[i+1] >= '0' && s[i+1] <= '9' && s[i+2] >= '0' && s[i+2] <= '9' {
			n, _ := strconv.Atoi(s[i : i+3])
			if n > 255 {
				return "", false
			}
			out.WriteByte(byte(n))
			i += 2
		} else {
			out.WriteByte(s[i])
		}
	}
	return out.String(), utf8.ValidString(out.String())
}

func discoveryEntry(e *zeroconf.ServiceEntry) (DiscoveredServer, bool) {
	if e == nil || e.Port < 1 || e.Port > 65535 || e.TTL == 0 {
		return DiscoveredServer{}, false
	}
	txt := map[string]string{}
	for _, s := range e.Text {
		s, valid := decodeDiscoveryTXT(s)
		if !valid {
			return DiscoveredServer{}, false
		}
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			continue
		}
		if _, exists := txt[k]; exists {
			return DiscoveredServer{}, false
		}
		txt[k] = v
	}
	id, err := hex.DecodeString(txt["id"])
	if err != nil || len(id) != 16 || txt["app"] != "fileindex" || txt["txtvers"] != "1" || txt["scheme"] != "http" || txt["path"] != "/" {
		return DiscoveredServer{}, false
	}
	name := strings.TrimSpace(txt["name"])
	if name == "" || len(name) > 180 || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || r == '/' }) {
		return DiscoveredServer{}, false
	}
	var ips []string
	for _, ip := range e.AddrIPv4 {
		if ip.To4() != nil && !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast() {
			ips = append(ips, ip.String())
		}
	}
	if len(ips) == 0 {
		return DiscoveredServer{}, false
	}
	sort.Strings(ips)
	return DiscoveredServer{ID: strings.ToLower(txt["id"]), Name: name, Addr: net.JoinHostPort(ips[0], strconv.Itoa(e.Port))}, true
}

// Discover browses until ctx ends. Each callback replaces the scan's complete
// snapshot. A new scan starts empty, so old/offline results do not accumulate.
func Discover(ctx context.Context, update func([]DiscoveredServer)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resolver, err := zeroconf.NewResolver(zeroconf.SelectIPTraffic(zeroconf.IPv4))
	if err != nil {
		return err
	}
	entries := make(chan *zeroconf.ServiceEntry, 32)
	if err := resolver.Browse(ctx, DiscoveryService, "local.", entries); err != nil {
		return err
	}
	found := map[string]DiscoveredServer{}
	// Drain until zeroconf closes the channel on cancellation, so its network
	// goroutine cannot get stuck sending a burst of answers after the UI closes.
	for e := range entries {
		if ctx.Err() != nil {
			continue
		}
		s, ok := discoveryEntry(e)
		if !ok {
			continue
		}
		// Same identity and endpoint is one row; multiple NICs may deliver
		// duplicate announcements. Choose a stable address for each ID.
		if old, exists := found[s.ID]; exists && old.Addr <= s.Addr {
			s.Addr = old.Addr
		}
		found[s.ID] = s
		list := make([]DiscoveredServer, 0, len(found))
		for _, v := range found {
			list = append(list, v)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Name == list[j].Name {
				return list[i].Addr < list[j].Addr
			}
			return list[i].Name < list[j].Name
		})
		update(list)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("局域网发现已停止，请重试")
}
