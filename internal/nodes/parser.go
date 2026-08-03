package nodes

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type ParsedNode struct {
	DisplayName string
	Protocol    string
	Server      string
	Port        int
	RawPayload  string
	Normalized  map[string]any
}

func ParseRawNodes(input string) ([]ParsedNode, []error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, []error{errors.New("input is empty")}
	}

	if parsed, err := parseYAMLNodes(input); err == nil && len(parsed) > 0 {
		return parsed, nil
	}

	var result []ParsedNode
	var errs []error
	scanner := bufio.NewScanner(strings.NewReader(input))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		node, err := ParseNodeURI(line)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", line, err))
			continue
		}
		result = append(result, node)
	}
	if len(result) == 0 && len(errs) == 0 {
		errs = append(errs, errors.New("no nodes parsed"))
	}
	return result, errs
}

func ParseNodeURI(raw string) (ParsedNode, error) {
	switch {
	case strings.HasPrefix(raw, "ss://"):
		return parseSS(raw)
	case strings.HasPrefix(raw, "trojan://"):
		return parseSimpleURLNode("trojan", raw)
	case strings.HasPrefix(raw, "vless://"):
		return parseSimpleURLNode("vless", raw)
	case strings.HasPrefix(raw, "hysteria2://"):
		return parseSimpleURLNode("hysteria2", raw)
	case strings.HasPrefix(raw, "tuic://"):
		return parseSimpleURLNode("tuic", raw)
	case strings.HasPrefix(raw, "vmess://"):
		return parseVMess(raw)
	default:
		return ParsedNode{}, fmt.Errorf("unsupported node uri")
	}
}

func NormalizeJSON(input map[string]any) string {
	data, _ := json.Marshal(input)
	return string(data)
}

func parseSS(raw string) (ParsedNode, error) {
	withoutScheme := strings.TrimPrefix(raw, "ss://")
	name := ""
	if idx := strings.Index(withoutScheme, "#"); idx >= 0 {
		name, _ = url.QueryUnescape(withoutScheme[idx+1:])
		withoutScheme = withoutScheme[:idx]
	}
	query := ""
	if idx := strings.Index(withoutScheme, "?"); idx >= 0 {
		query = withoutScheme[idx+1:]
		withoutScheme = withoutScheme[:idx]
	}
	decoded := withoutScheme
	if !strings.Contains(decoded, "@") {
		b, err := base64.RawURLEncoding.DecodeString(withoutScheme)
		if err != nil {
			b, err = base64.StdEncoding.DecodeString(withoutScheme)
			if err != nil {
				return ParsedNode{}, fmt.Errorf("invalid ss payload: %w", err)
			}
		}
		decoded = string(b)
	}
	auth, endpoint, ok := strings.Cut(decoded, "@")
	if !ok {
		return ParsedNode{}, errors.New("invalid ss structure")
	}
	method, password, ok := strings.Cut(auth, ":")
	if !ok {
		return ParsedNode{}, errors.New("invalid ss auth")
	}
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		return ParsedNode{}, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return ParsedNode{}, err
	}
	if name == "" {
		name = host
	}
	normalized := map[string]any{
		"name":     name,
		"type":     "ss",
		"server":   host,
		"port":     port,
		"cipher":   method,
		"password": password,
	}
	copyQuery(normalized, query)
	return ParsedNode{
		DisplayName: name,
		Protocol:    "ss",
		Server:      host,
		Port:        port,
		RawPayload:  raw,
		Normalized:  normalized,
	}, nil
}

func parseVMess(raw string) (ParsedNode, error) {
	payload := strings.TrimPrefix(raw, "vmess://")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return ParsedNode{}, fmt.Errorf("invalid vmess payload: %w", err)
		}
	}
	var data map[string]any
	if err := json.Unmarshal(decoded, &data); err != nil {
		return ParsedNode{}, err
	}
	server := strings.TrimSpace(stringValue(data["add"]))
	if server == "" {
		return ParsedNode{}, errors.New("vmess server is required")
	}
	name := strings.TrimSpace(stringValue(data["ps"]))
	if name == "" {
		name = server
	}
	port, err := toInt(data["port"])
	if err != nil || !isValidPort(port) {
		return ParsedNode{}, errors.New("vmess port must be between 1 and 65535")
	}
	data["name"] = name
	data["type"] = "vmess"
	data["server"] = server
	data["port"] = port
	return ParsedNode{
		DisplayName: name,
		Protocol:    "vmess",
		Server:      server,
		Port:        port,
		RawPayload:  raw,
		Normalized:  data,
	}, nil
}

func parseSimpleURLNode(protocol, raw string) (ParsedNode, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return ParsedNode{}, err
	}
	if !strings.EqualFold(u.Scheme, protocol) {
		return ParsedNode{}, fmt.Errorf("invalid %s scheme", protocol)
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return ParsedNode{}, fmt.Errorf("%s server is required", protocol)
	}
	if strings.TrimSpace(u.Port()) == "" {
		return ParsedNode{}, fmt.Errorf("%s port is required", protocol)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || !isValidPort(port) {
		return ParsedNode{}, fmt.Errorf("%s port must be between 1 and 65535", protocol)
	}
	if u.User == nil || strings.TrimSpace(u.User.Username()) == "" {
		return ParsedNode{}, fmt.Errorf("%s credential is required", protocol)
	}
	name := u.Fragment
	if name == "" {
		name = host
	}
	normalized := map[string]any{
		"name":   name,
		"type":   protocol,
		"server": host,
		"port":   port,
	}
	if u.User != nil {
		if username := u.User.Username(); username != "" {
			if protocol == "trojan" {
				normalized["password"] = username
			} else {
				normalized["uuid"] = username
			}
		}
		if password, ok := u.User.Password(); ok {
			normalized["password"] = password
		}
	}
	for key, values := range u.Query() {
		if key == "type" {
			if len(values) == 1 {
				normalized["network"] = values[0]
			} else {
				normalized["network"] = values
			}
			continue
		}
		if len(values) == 1 {
			normalized[key] = values[0]
		} else {
			normalized[key] = values
		}
	}
	return ParsedNode{
		DisplayName: name,
		Protocol:    protocol,
		Server:      host,
		Port:        port,
		RawPayload:  raw,
		Normalized:  normalized,
	}, nil
}

func parseYAMLNodes(input string) ([]ParsedNode, error) {
	var wrapper struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(input), &wrapper); err == nil && len(wrapper.Proxies) > 0 {
		return normalizeProxyMaps(wrapper.Proxies)
	}
	var list []map[string]any
	if err := yaml.Unmarshal([]byte(input), &list); err == nil && len(list) > 0 {
		return normalizeProxyMaps(list)
	}
	return nil, errors.New("not yaml proxies")
}

func normalizeProxyMaps(items []map[string]any) ([]ParsedNode, error) {
	var result []ParsedNode
	for _, item := range items {
		name := fmt.Sprint(item["name"])
		protocol := fmt.Sprint(item["type"])
		server := fmt.Sprint(item["server"])
		port, _ := toInt(item["port"])
		if name == "" || protocol == "" || server == "" || port == 0 {
			continue
		}
		raw, _ := yaml.Marshal(item)
		result = append(result, ParsedNode{
			DisplayName: name,
			Protocol:    protocol,
			Server:      server,
			Port:        port,
			RawPayload:  strings.TrimSpace(string(raw)),
			Normalized:  item,
		})
	}
	if len(result) == 0 {
		return nil, errors.New("no valid proxy items")
	}
	return result, nil
}

func copyQuery(target map[string]any, rawQuery string) {
	if rawQuery == "" {
		return
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return
	}
	for key, items := range values {
		if len(items) == 1 {
			target[key] = items[0]
		} else {
			target[key] = items
		}
	}
}

func toInt(v any) (int, error) {
	switch value := v.(type) {
	case int:
		return value, nil
	case int64:
		return int(value), nil
	case float64:
		return int(value), nil
	case string:
		return strconv.Atoi(value)
	default:
		return 0, fmt.Errorf("unsupported numeric type %T", v)
	}
}

func stringValue(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	default:
		return fmt.Sprint(value)
	}
}

func isValidPort(port int) bool {
	return port >= 1 && port <= 65535
}
