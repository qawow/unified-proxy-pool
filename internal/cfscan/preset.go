package cfscan

import (
	"fmt"
	"net"
	"strings"
)

// OfficialIPv4 is a snapshot of https://www.cloudflare.com/ips-v4
// (Cloudflare anycast edges). Scanning these finds real CF colo IPs for 优选,
// not Reality-fallback hosts. Large prefixes must be sampled; expanding
// 104.16.0.0/13 would blow MaxTargets.
var OfficialIPv4 = []string{
	"173.245.48.0/20",
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"141.101.64.0/18",
	"108.162.192.0/18",
	"190.93.240.0/20",
	"188.114.96.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"162.158.0.0/15",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"172.64.0.0/13",
	"131.0.72.0/22",
}

const DefaultSampleSlash24 = 32

func OfficialCIDRText() string {
	return "# Cloudflare 官方 IPv4 段（https://www.cloudflare.com/ips-v4）\n" +
		"# 整段展开会超过扫描上限，请用「填入 /24 抽样」或自己改小前缀\n" +
		strings.Join(OfficialIPv4, "\n") + "\n"
}

// SampleSlash24s returns up to n disjoint /24s drawn from OfficialIPv4.
func SampleSlash24s(n int) string {
	if n <= 0 {
		n = DefaultSampleSlash24
	}
	var lines []string
	for _, cidr := range OfficialIPv4 {
		if len(lines) >= n {
			break
		}
		ip, ipnet, err := net.ParseCIDR(cidr)
		if err != nil || ip.To4() == nil {
			continue
		}
		ones, _ := ipnet.Mask.Size()
		start := make(net.IP, 4)
		copy(start, ip.To4().Mask(ipnet.Mask))
		// How many /24s fit in this prefix.
		count := 1
		if ones < 24 {
			count = 1 << uint(24-ones)
		}
		step := 1
		if count > n-len(lines) && n-len(lines) > 0 {
			// stride so we sample across the prefix, not only the first /24s
			step = count / (n - len(lines))
			if step < 1 {
				step = 1
			}
		}
		for i := 0; i < count && len(lines) < n; i += step {
			cur := make(net.IP, 4)
			copy(cur, start)
			addIPv4(cur, uint32(i)<<8)
			if !ipnet.Contains(cur) {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s/24", cur.String()))
		}
	}
	return "# Cloudflare 官方段抽样 /24（约 " + fmt.Sprintf("%d", len(lines)*256) + " 个 IP）\n" +
		strings.Join(lines, "\n") + "\n"
}
