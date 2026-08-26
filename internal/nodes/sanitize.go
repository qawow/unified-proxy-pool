package nodes

import (
	"fmt"
	"strings"
	"unicode"
)

// ssCiphers is the set mihomo/shadowsocks actually initialize.
// Anything else becomes: cipher: <garbage> initialize error: unknown method
// and kills the whole YAML parse.
var ssCiphers = map[string]struct{}{
	"none": {}, "dummy": {},
	"aes-128-gcm": {}, "aes-192-gcm": {}, "aes-256-gcm": {},
	"chacha20-ietf-poly1305": {}, "xchacha20-ietf-poly1305": {},
	"2022-blake3-aes-128-gcm": {}, "2022-blake3-aes-256-gcm": {},
	"2022-blake3-chacha20-poly1305": {},
	"aes-128-cfb": {}, "aes-192-cfb": {}, "aes-256-cfb": {},
	"aes-128-ctr": {}, "aes-192-ctr": {}, "aes-256-ctr": {},
	"aes-128-ofb": {}, "aes-192-ofb": {}, "aes-256-ofb": {},
	"camellia-128-cfb": {}, "camellia-192-cfb": {}, "camellia-256-cfb": {},
	"chacha20-ietf": {}, "xchacha20": {}, "chacha20": {}, "salsa20": {},
	"rc4-md5": {}, "rc4": {},
	"bf-cfb": {}, "cast5-cfb": {}, "des-cfb": {}, "idea-cfb": {}, "rc2-cfb": {}, "seed-cfb": {},
	"lea-128-gcm": {}, "lea-192-gcm": {}, "lea-256-gcm": {},
	"aes-128-ccm": {}, "aes-192-ccm": {}, "aes-256-ccm": {},
	"2022-blake3-chacha8-poly1305": {},
}

func IsKnownSSCipher(cipher string) bool {
	c := strings.ToLower(strings.TrimSpace(cipher))
	if c == "" {
		return false
	}
	_, ok := ssCiphers[c]
	return ok
}

// SanitizeProxyMap mutates a mihomo proxy object so one bad field cannot
// abort parsing of the whole config. Returns an error if the node must be dropped.
func SanitizeProxyMap(payload map[string]any) error {
	if payload == nil {
		return fmt.Errorf("empty proxy")
	}
	trimQueryJunk(payload)
	coerceALPN(payload)
	typ := strings.ToLower(strings.TrimSpace(fmt.Sprint(payload["type"])))
	switch typ {
	case "ss", "ssr":
		cipher := printableCipher(payload["cipher"])
		if !IsKnownSSCipher(cipher) {
			return fmt.Errorf("ss cipher %q is not a known method", cipher)
		}
		payload["cipher"] = cipher
	case "vless":
		if err := sanitizeVLESSEncryption(payload); err != nil {
			return err
		}
		sanitizeEnumField(payload, "flow", vlessFlows)
	}
	server := strings.TrimSpace(fmt.Sprint(payload["server"]))
	if server == "" || server == "<nil>" {
		return fmt.Errorf("missing server")
	}
	return nil
}

func printableCipher(v any) string {
	switch t := v.(type) {
	case string:
		if !isPrintableASCII(t) {
			return ""
		}
		return strings.TrimSpace(t)
	case []byte:
		s := string(t)
		if !isPrintableASCII(s) {
			return ""
		}
		return strings.TrimSpace(s)
	default:
		s := strings.TrimSpace(fmt.Sprint(v))
		if !isPrintableASCII(s) {
			return ""
		}
		return s
	}
}

func isPrintableASCII(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r > unicode.MaxASCII || r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// vlessEncryption is what mihomo accepts. "none=" from broken subscribe
// query strings is not in this set until cleaned.
var vlessEncryption = map[string]struct{}{
	"none": {},
	"mlkem768x25519plus": {},
}

var vlessFlows = map[string]struct{}{
	"": {},
	"xtls-rprx-vision": {},
	"xtls-rprx-vision-udp443": {},
}

// Keys that subscriptions often leave as "none=" / "tcp=" from broken query
// parsing. Stripping trailing =&; here stops the next enum field from
// reaching mihomo uncleaned.
var queryJunkKeys = []string{
	"encryption", "flow", "network", "security", "packet-encoding",
	"client-fingerprint", "cipher", "udp",
}

func trimQueryJunk(payload map[string]any) {
	for _, key := range queryJunkKeys {
		raw, ok := payload[key]
		if !ok || raw == nil {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			continue
		}
		cleaned := strings.TrimRight(strings.TrimSpace(s), "=&;")
		cleaned = strings.TrimSpace(cleaned)
		payload[key] = cleaned
	}
}

func sanitizeVLESSEncryption(payload map[string]any) error {
	raw, ok := payload["encryption"]
	if !ok || raw == nil {
		return nil
	}
	s := strings.TrimSpace(fmt.Sprint(raw))
	if s == "" || s == "<nil>" {
		delete(payload, "encryption")
		return nil
	}
	cleaned := strings.TrimRight(strings.TrimSpace(s), "=&;")
	cleaned = strings.ToLower(strings.TrimSpace(cleaned))
	if cleaned == "" {
		cleaned = "none"
	}
	if _, ok := vlessEncryption[cleaned]; !ok {
		return fmt.Errorf("invalid vless encryption %q", s)
	}
	payload["encryption"] = cleaned
	return nil
}

func sanitizeEnumField(payload map[string]any, key string, allowed map[string]struct{}) {
	raw, ok := payload[key]
	if !ok || raw == nil {
		return
	}
	s := strings.TrimRight(strings.TrimSpace(fmt.Sprint(raw)), "=&;")
	s = strings.TrimSpace(s)
	if _, ok := allowed[s]; !ok {
		delete(payload, key)
		return
	}
	if s == "" {
		delete(payload, key)
		return
	}
	payload[key] = s
}

func coerceALPN(payload map[string]any) {
	raw, ok := payload["alpn"]
	if !ok || raw == nil {
		return
	}
	if list := alpnList(raw); len(list) > 0 {
		payload["alpn"] = list
	} else {
		delete(payload, "alpn")
	}
}

func alpnList(v any) []string {
	switch t := v.(type) {
	case []string:
		return cleanALPN(t)
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, fmt.Sprint(item))
		}
		return cleanALPN(out)
	case string:
		parts := strings.FieldsFunc(t, func(r rune) bool {
			return r == ',' || r == ';' || r == ' '
		})
		return cleanALPN(parts)
	default:
		return nil
	}
}

func cleanALPN(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || !isPrintableASCII(s) {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
