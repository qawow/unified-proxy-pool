package nodes

import "strings"

// applySSPlugin converts SIP002's plugin string into mihomo's plugin +
// plugin-opts pair. The raw "plugin=obfs-local;obfs=http;obfs-host=x" value
// means nothing to mihomo, so obfuscated ss nodes were imported without their
// obfuscation and then failed to connect.
func applySSPlugin(p map[string]any) {
	raw := str(p["plugin"])
	if raw == "" {
		return
	}
	parts := strings.Split(raw, ";")
	name := strings.TrimSpace(parts[0])
	opts := map[string]any{}
	for _, kv := range parts[1:] {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if !ok {
			opts[k] = true
			continue
		}
		opts[k] = strings.TrimSpace(v)
	}
	switch {
	case strings.HasPrefix(name, "obfs"):
		p["plugin"] = "obfs"
		// mihomo names it "mode"; SIP002 calls it "obfs".
		if v, ok := opts["obfs"]; ok {
			opts["mode"] = v
			delete(opts, "obfs")
		}
		if v, ok := opts["obfs-host"]; ok {
			opts["host"] = v
			delete(opts, "obfs-host")
		}
		if _, ok := opts["mode"]; !ok {
			// mihomo aborts the whole config with "obfs mode error" when mode is
			// missing, so drop the plugin rather than the node.
			delete(p, "plugin")
			return
		}
	case strings.HasPrefix(name, "v2ray-plugin"):
		p["plugin"] = "v2ray-plugin"
		if _, ok := opts["mode"]; !ok {
			opts["mode"] = "websocket"
		}
		if v, ok := opts["tls"]; ok {
			opts["tls"] = v == true || v == "true" || v == "1"
		}
	default:
		// Unknown plugin: mihomo would reject it, and the node still works
		// without one more often than not.
		delete(p, "plugin")
		return
	}
	if len(opts) > 0 {
		p["plugin-opts"] = opts
	}
}
