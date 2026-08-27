package geoip

import (
	"strings"
	"sync"
)

// Filter is the process-wide country deny list. applyFeatureHot installs it;
// ingest, pick, validate and mihomo publish all read Active() so a CN node
// cannot sneak through a second path.
type Filter struct {
	Enabled   bool
	CheckExit bool
	Blocked   []string
	blocked   map[string]struct{}
}

var (
	filterMu sync.RWMutex
	active   = DefaultFilter()
)

// DefaultFilter permanently drops mainland China. HK/TW/MO stay unless the
// operator adds them. An empty Blocked list with Enabled=true blocks nothing.
func DefaultFilter() Filter {
	return BuildFilter(true, true, []string{"CN"})
}

func BuildFilter(enabled, checkExit bool, codes []string) Filter {
	return newFilter(enabled, checkExit, codes)
}

func newFilter(enabled, checkExit bool, codes []string) Filter {
	f := Filter{Enabled: enabled, CheckExit: checkExit, Blocked: make([]string, 0, len(codes))}
	f.blocked = map[string]struct{}{}
	seen := map[string]struct{}{}
	for _, c := range codes {
		n := Normalize(c)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		f.Blocked = append(f.Blocked, n)
		f.blocked[n] = struct{}{}
	}
	return f
}

func SetFilter(f Filter) {
	if f.blocked == nil {
		f = newFilter(f.Enabled, f.CheckExit, f.Blocked)
	}
	filterMu.Lock()
	active = f
	filterMu.Unlock()
}

func Active() Filter {
	filterMu.RLock()
	defer filterMu.RUnlock()
	return active
}

func (f Filter) Blocks(region string) bool {
	if !f.Enabled {
		return false
	}
	code := Normalize(region)
	if code == "" {
		return false
	}
	_, ok := f.blocked[code]
	return ok
}

// BlockedNode reports whether a subscription/manual node should be dropped.
// Unknown region is allowed: only a positive CN (or other blocked) signal drops it.
func (f Filter) BlockedNode(server, displayName string) bool {
	if !f.Enabled {
		return false
	}
	if f.Blocks(GuessFromLabel(displayName)) {
		return true
	}
	if f.Blocks(GuessFromHost(server)) {
		return true
	}
	return false
}

var aliases = map[string]string{
	"cn": "CN", "china": "CN", "prc": "CN",
	"中国": "CN", "中國": "CN", "中华人民共和国": "CN", "中華人民共和國": "CN",
	"大陆": "CN", "大陸": "CN", "国内": "CN", "國內": "CN",
	"hk": "HK", "hongkong": "HK", "hong kong": "HK", "香港": "HK",
	"tw": "TW", "taiwan": "TW", "台湾": "TW", "台灣": "TW", "臺灣": "TW",
	"mo": "MO", "macao": "MO", "macau": "MO", "澳门": "MO", "澳門": "MO",
	"us": "US", "usa": "US", "united states": "US", "美国": "US", "美國": "US",
	"jp": "JP", "japan": "JP", "日本": "JP",
	"sg": "SG", "singapore": "SG", "新加坡": "SG",
	"kr": "KR", "korea": "KR", "south korea": "KR", "韩国": "KR", "韓國": "KR",
	"de": "DE", "germany": "DE", "德国": "DE", "德國": "DE",
	"gb": "GB", "uk": "GB", "united kingdom": "GB", "英国": "GB", "英國": "GB",
	"ru": "RU", "russia": "RU", "俄罗斯": "RU", "俄羅斯": "RU",
	"in": "IN", "india": "IN", "印度": "IN",
	"id": "ID", "indonesia": "ID", "印尼": "ID", "印度尼西亚": "ID",
	"vn": "VN", "vietnam": "VN", "越南": "VN",
	"br": "BR", "brazil": "BR", "巴西": "BR",
	"nl": "NL", "netherlands": "NL", "荷兰": "NL", "荷蘭": "NL",
	"fr": "FR", "france": "FR", "法国": "FR", "法國": "FR",
	"au": "AU", "australia": "AU", "澳大利亚": "AU", "澳洲": "AU",
}

// Normalize maps "中国" / "china" / "cn" onto ISO 3166-1 alpha-2. Unknown
// two-letter tokens are uppercased; anything else is dropped.
func Normalize(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if code, ok := aliases[strings.ToLower(s)]; ok {
		return code
	}
	if code, ok := aliases[s]; ok {
		return code
	}
	if len(s) == 2 {
		a, b := s[0], s[1]
		if isLetter(a) && isLetter(b) {
			return strings.ToUpper(s)
		}
	}
	return ""
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// GuessFromLabel reads clash-style names ("美国 01", "中国 上海").
// "CN2" is a GIA product name and must not count as mainland China.
// HK/TW/MO are checked first so "中国香港" is HK.
func GuessFromLabel(name string) string {
	s := strings.TrimSpace(name)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	switch {
	case containsAny(s, "香港") || strings.Contains(lower, "hong kong") || strings.Contains(lower, "hongkong"):
		return "HK"
	case containsAny(s, "台湾", "台灣", "臺灣") || strings.Contains(lower, "taiwan"):
		return "TW"
	case containsAny(s, "澳门", "澳門") || strings.Contains(lower, "macao") || strings.Contains(lower, "macau"):
		return "MO"
	case containsAny(s, "中国", "中國", "大陆", "大陸", "国内", "國內"):
		return "CN"
	}
	return ""
}

// GuessFromHost only fires on a clear ccTLD. Bare IPs are unknown until GeoIP.
func GuessFromHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	switch {
	case strings.HasSuffix(h, ".cn") || strings.HasSuffix(h, ".中国") || strings.HasSuffix(h, ".xn--fiqs8s"):
		return "CN"
	case strings.HasSuffix(h, ".hk") || strings.HasSuffix(h, ".香港"):
		return "HK"
	case strings.HasSuffix(h, ".tw") || strings.HasSuffix(h, ".台湾") || strings.HasSuffix(h, ".台灣"):
		return "TW"
	case strings.HasSuffix(h, ".mo"):
		return "MO"
	}
	return ""
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
