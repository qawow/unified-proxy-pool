package nodes

import (
	"fmt"
	"strings"
)

// translateShareFields rewrites the keys used by share links (v2rayN vmess
// JSON, vless/trojan/hysteria2 query strings) into the ones mihomo actually
// reads.
//
// Without this, an imported TLS or WebSocket node is published as plaintext
// TCP: mihomo does not know `net`, `security`, `sni`, `pbk`, `sid` or `fp`, and
// silently ignores them, so the node either fails to connect or connects
// without the transport the operator configured. Reality nodes never worked at
// all when imported by URI.
func translateShareFields(p map[string]any) {
	if p == nil {
		return
	}
	typ := strings.ToLower(strings.TrimSpace(str(p["type"])))

	// --- transport ------------------------------------------------------
	// vmess JSON calls it "net"; the URI form calls it "type" (already mapped
	// to "network" by the caller).
	if v := str(p["net"]); v != "" {
		if _, ok := p["network"]; !ok {
			p["network"] = v
		}
		delete(p, "net")
	}
	network := strings.ToLower(strings.TrimSpace(str(p["network"])))
	if network == "h2" {
		p["network"] = "h2"
		network = "h2"
	}

	host := str(p["host"])
	path := str(p["path"])
	serviceName := firstNonEmptyStr(str(p["serviceName"]), str(p["servicename"]))

	switch network {
	case "ws":
		opts := mapOpt(p, "ws-opts")
		if path != "" {
			setIfAbsent(opts, "path", path)
		}
		if host != "" {
			headers, _ := opts["headers"].(map[string]any)
			if headers == nil {
				headers = map[string]any{}
				opts["headers"] = headers
			}
			setIfAbsent(headers, "Host", host)
		}
		if len(opts) > 0 {
			p["ws-opts"] = opts
		}
	case "grpc":
		opts := mapOpt(p, "grpc-opts")
		name := firstNonEmptyStr(serviceName, path)
		if name != "" {
			setIfAbsent(opts, "grpc-service-name", strings.TrimPrefix(name, "/"))
		}
		if len(opts) > 0 {
			p["grpc-opts"] = opts
		}
	case "h2":
		opts := mapOpt(p, "h2-opts")
		if path != "" {
			setIfAbsent(opts, "path", path)
		}
		if host != "" {
			setIfAbsent(opts, "host", []any{host})
		}
		if len(opts) > 0 {
			p["h2-opts"] = opts
		}
	case "http":
		opts := mapOpt(p, "http-opts")
		if path != "" {
			setIfAbsent(opts, "path", []any{path})
		}
		if host != "" {
			headers, _ := opts["headers"].(map[string]any)
			if headers == nil {
				headers = map[string]any{}
				opts["headers"] = headers
			}
			setIfAbsent(headers, "Host", []any{host})
		}
		if len(opts) > 0 {
			p["http-opts"] = opts
		}
	}
	delete(p, "serviceName")
	delete(p, "servicename")
	// "host" and "path" are transport-level in share links; leaving them at the
	// top level would be meaningless to mihomo.
	if network != "" {
		delete(p, "host")
		delete(p, "path")
	}

	// --- TLS ------------------------------------------------------------
	security := strings.ToLower(strings.TrimSpace(str(p["security"])))
	if security == "" {
		// vmess JSON puts "tls" (or "") in a field literally named tls.
		if v := strings.ToLower(strings.TrimSpace(str(p["tls"]))); v == "tls" || v == "reality" || v == "xtls" {
			security = v
		}
	}
	switch security {
	case "tls", "xtls", "reality":
		p["tls"] = true
	case "none":
		delete(p, "tls")
	}
	delete(p, "security")

	if sni := firstNonEmptyStr(str(p["sni"]), str(p["peer"])); sni != "" {
		setIfAbsent(p, "servername", sni)
	}
	delete(p, "sni")
	delete(p, "peer")

	if fp := str(p["fp"]); fp != "" {
		setIfAbsent(p, "client-fingerprint", fp)
	}
	delete(p, "fp")

	if security == "reality" {
		opts := mapOpt(p, "reality-opts")
		if pbk := str(p["pbk"]); pbk != "" {
			setIfAbsent(opts, "public-key", pbk)
		}
		if sid := str(p["sid"]); sid != "" {
			setIfAbsent(opts, "short-id", sid)
		}
		if len(opts) > 0 {
			p["reality-opts"] = opts
		}
	}
	delete(p, "pbk")
	delete(p, "sid")
	delete(p, "spx")

	for _, key := range []string{"allowInsecure", "allowinsecure", "insecure"} {
		if v, ok := p[key]; ok {
			s := strings.ToLower(fmt.Sprint(v))
			if s == "1" || s == "true" {
				setIfAbsent(p, "skip-cert-verify", true)
			}
			delete(p, key)
		}
	}

	// --- vmess specifics -------------------------------------------------
	if typ == "vmess" {
		if v, ok := p["aid"]; ok {
			setIfAbsent(p, "alterId", v)
			delete(p, "aid")
		}
		if v := str(p["scy"]); v != "" {
			setIfAbsent(p, "cipher", v)
		}
		delete(p, "scy")
		// v2rayN writes the uuid as "id".
		if v := str(p["id"]); v != "" {
			setIfAbsent(p, "uuid", v)
			delete(p, "id")
		}
		// Fields that only mean something to the v2rayN client.
		for _, k := range []string{"v", "ps", "add", "headerType", "fp2", "sni2"} {
			delete(p, k)
		}
	}
}

func mapOpt(p map[string]any, key string) map[string]any {
	if existing, ok := p[key].(map[string]any); ok && existing != nil {
		return existing
	}
	return map[string]any{}
}

func setIfAbsent(m map[string]any, key string, value any) {
	if _, ok := m[key]; ok {
		return
	}
	m[key] = value
}

func str(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
