package probe

import (
	"encoding/json"

	"unified-proxy-pool/internal/models"
)

// nodeCredentials pulls username/password out of a node's normalized config so
// authenticated http/socks5 nodes can actually be probed. Without them every
// such node reported "unavailable" no matter its real state.
func nodeCredentials(node models.RuntimeNode) (user, pass string) {
	if node.NormalizedJSON == "" {
		return "", ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(node.NormalizedJSON), &m); err != nil {
		return "", ""
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	return str("username", "user"), str("password", "pass")
}
