package netutil

import (
	"net"
	"strconv"
	"strings"
)

// LANIPs returns non-loopback IPv4 addresses suitable for LAN client access.
func LANIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip := extractIPv4(addr)
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			s := ip.String()
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// GlobalIPv6 returns non-loopback, non-link-local IPv6 addresses currently
// assigned. Empty means this host cannot originate global IPv6 (no HE tunnel,
// no native prefix).
func GlobalIPv6() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.To4() != nil {
				continue
			}
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsInterfaceLocalMulticast() {
				continue
			}
			s := ip.String()
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

func extractIPv4(addr net.Addr) net.IP {
	switch v := addr.(type) {
	case *net.IPNet:
		return v.IP.To4()
	case *net.IPAddr:
		return v.IP.To4()
	default:
		return nil
	}
}

// PreferLANIP picks a preferred LAN IP (private ranges first).
func PreferLANIP() string {
	ips := LANIPs()
	if len(ips) == 0 {
		return ""
	}
	var fallback string
	for _, ip := range ips {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			continue
		}
		if parsed.IsPrivate() {
			return ip
		}
		if fallback == "" {
			fallback = ip
		}
	}
	if fallback != "" {
		return fallback
	}
	return ips[0]
}

// HostPort rewrites listen addr host for client-facing URL.
// 0.0.0.0 / :: / empty -> preferred LAN IP (or 127.0.0.1 as last resort).
func ClientHostPort(listenAddr, preferredHost string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		// maybe bare port
		if strings.HasPrefix(listenAddr, ":") {
			port = strings.TrimPrefix(listenAddr, ":")
			host = ""
		} else {
			return listenAddr
		}
	}
	if preferredHost == "" {
		preferredHost = PreferLANIP()
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		if preferredHost == "" {
			preferredHost = "127.0.0.1"
		}
		host = preferredHost
	}
	if port == "" {
		return host
	}
	// ensure port numeric
	if _, err := strconv.Atoi(port); err != nil {
		return net.JoinHostPort(host, port)
	}
	return net.JoinHostPort(host, port)
}

// ClientEndpoints builds http/socks5 LAN endpoints for docs/UI.
func ClientEndpoints(listenAddr string) map[string]string {
	hostport := ClientHostPort(listenAddr, "")
	return map[string]string{
		"http":   "http://" + hostport,
		"https":  "http://" + hostport,
		"socks5": "socks5://" + hostport,
		"host":   hostport,
	}
}
