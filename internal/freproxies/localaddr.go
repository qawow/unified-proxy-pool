package freproxies

import (
	"net"
	"strings"
	"sync"
)

// isLocalAddr reports whether addr points at this host or the local network.
//
// Scraped proxy lists come from the open internet, so an entry like
// "127.0.0.1:7891" or "192.168.2.198:9090" is poisoning or junk: dialling one
// turns the pool into a confused deputy against services inside the network.
//
// Submitted addresses are deliberately NOT filtered by this — running a local
// mihomo/clash instance and pushing it into the pool is a supported workflow
// (scripts/clash_deploy.sh does exactly that). Those go through selfAddr below,
// which only rejects the pool's own listeners.
func isLocalAddr(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		// Hostnames are not resolved here: the pool only ever stores literals,
		// and a DNS lookup per scraped line would be a free amplifier.
		return strings.EqualFold(host, "localhost")
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast()
}

var selfPorts = struct {
	mu sync.RWMutex
	m  map[int]struct{}
}{m: map[int]struct{}{}}

// SetSelfPorts records the ports this process listens on (panel, single-hop,
// chain). An entry pointing at one of them would make the pool dial itself:
// either an infinite proxy loop, or a request to the panel that arrives with a
// loopback peer address and so counts as LAN.
func SetSelfPorts(ports ...int) {
	selfPorts.mu.Lock()
	defer selfPorts.mu.Unlock()
	selfPorts.m = make(map[int]struct{}, len(ports))
	for _, p := range ports {
		if p > 0 {
			selfPorts.m[p] = struct{}{}
		}
	}
}

// isSelfAddr reports whether addr is one of this process's own listeners.
func isSelfAddr(addr string) bool {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	port := 0
	for _, c := range portText {
		if c < '0' || c > '9' {
			return false
		}
		port = port*10 + int(c-'0')
	}
	selfPorts.mu.RLock()
	_, hit := selfPorts.m[port]
	selfPorts.mu.RUnlock()
	if !hit {
		return false
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return strings.EqualFold(host, "localhost")
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	return isOwnIP(ip)
}

func isOwnIP(ip net.IP) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}
