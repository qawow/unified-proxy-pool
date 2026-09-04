package cfscan

import (
	"fmt"
	"net"
	"strings"
)

const MaxTargets = 20000

// ParseTargets expands IPv4 addresses and CIDRs from a text dump.
// Lines starting with # are comments. IPv6 and CIDRs larger than MaxTargets
// after expansion are rejected rather than silently truncated mid-range.
func ParseTargets(text string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, 256)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, ":") && !strings.Contains(line, ".") {
			return nil, fmt.Errorf("ipv6 not supported: %s", line)
		}
		if strings.Contains(line, "/") {
			ips, err := expandCIDR(line)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if _, ok := seen[ip]; ok {
					continue
				}
				if len(out) >= MaxTargets {
					return nil, fmt.Errorf("more than %d addresses; split the list", MaxTargets)
				}
				seen[ip] = struct{}{}
				out = append(out, ip)
			}
			continue
		}
		ip := net.ParseIP(line)
		if ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("not an ipv4 address: %s", line)
		}
		v := ip.To4().String()
		if _, ok := seen[v]; ok {
			continue
		}
		if len(out) >= MaxTargets {
			return nil, fmt.Errorf("more than %d addresses; split the list", MaxTargets)
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no ipv4 addresses")
	}
	return out, nil
}

func expandCIDR(cidr string) ([]string, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("bad cidr %s: %w", cidr, err)
	}
	if ip.To4() == nil {
		return nil, fmt.Errorf("ipv6 cidr not supported: %s", cidr)
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 || ones < 0 {
		return nil, fmt.Errorf("bad mask: %s", cidr)
	}
	n := 1 << uint(32-ones)
	if n > MaxTargets {
		return nil, fmt.Errorf("%s expands to %d hosts (max %d); use a smaller prefix", cidr, n, MaxTargets)
	}
	out := make([]string, 0, n)
	cur := ip.To4()
	if cur == nil {
		return nil, fmt.Errorf("not ipv4: %s", cidr)
	}
	start := make(net.IP, 4)
	copy(start, cur.Mask(ipnet.Mask))
	for i := 0; i < n; i++ {
		cand := make(net.IP, 4)
		copy(cand, start)
		addIPv4(cand, uint32(i))
		if ipnet.Contains(cand) {
			out = append(out, cand.String())
		}
	}
	return out, nil
}

func addIPv4(ip net.IP, n uint32) {
	v := uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
	v += n
	ip[0] = byte(v >> 24)
	ip[1] = byte(v >> 16)
	ip[2] = byte(v >> 8)
	ip[3] = byte(v)
}
