package pools

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"log"

	"gopkg.in/yaml.v3"

	"unified-proxy-pool/internal/config"
	"unified-proxy-pool/internal/geoip"
	"unified-proxy-pool/internal/models"
	"unified-proxy-pool/internal/nodes"
)

type PublishBundle struct {
	ProdConfig  []byte
	ProbeConfig []byte
}

const MaxProbeSpeedSlots = config.MaxProbeSpeedSlots

func BuildPublishBundle(secret, prodController, probeController string, probeMixedPort int, testURL, logLevel string, poolList []models.ProxyPool, members map[int64][]models.RuntimeNode, inventory []models.RuntimeNode) (PublishBundle, error) {
	prodConfig, err := buildProdConfig(secret, prodController, testURL, logLevel, poolList, members)
	if err != nil {
		return PublishBundle{}, err
	}
	probeConfig, err := buildProbeConfig(secret, probeController, probeMixedPort, logLevel, inventory)
	if err != nil {
		return PublishBundle{}, err
	}
	return PublishBundle{
		ProdConfig:  prodConfig,
		ProbeConfig: probeConfig,
	}, nil
}

func BuildProbeInventoryConfig(secret, probeController string, probeMixedPort int, logLevel string, inventory []models.RuntimeNode) ([]byte, error) {
	return buildProbeConfig(secret, probeController, probeMixedPort, logLevel, inventory)
}

func RuntimeNodeName(node models.RuntimeNode) string {
	return runtimeNodeName(node)
}

func ProbeSpeedSlotGroupName(slotIndex int) string {
	return probeSpeedSlotGroupName(slotIndex)
}

func ProbeSpeedSlotPort(probeMixedPort, slotIndex int) int {
	return probeSpeedSlotPort(probeMixedPort, slotIndex)
}

func buildProdConfig(secret, controller, testURL, logLevel string, poolList []models.ProxyPool, members map[int64][]models.RuntimeNode) ([]byte, error) {
	type listener struct {
		Name   string              `yaml:"name"`
		Type   string              `yaml:"type"`
		Listen string              `yaml:"listen"`
		Port   int                 `yaml:"port"`
		Proxy  string              `yaml:"proxy"`
		Users  []map[string]string `yaml:"users,omitempty"`
	}

	root := map[string]any{
		"mode":                "rule",
		"log-level":           config.NormalizeLogLevel(logLevel),
		"allow-lan":           true,
		"external-controller": controller,
		"secret":              secret,
		"proxies":             []map[string]any{},
		"proxy-groups":        []map[string]any{},
		"listeners":           []listener{},
		"rules":               []string{"MATCH,DIRECT"},
	}
	seenProxyNames := make(map[string]struct{})

	for _, pool := range poolList {
		if !pool.Enabled {
			continue
		}
		groupName := poolGroupName(pool.ID)
		groupMembers := members[pool.ID]
		memberNames := make([]string, 0, len(groupMembers))
		for _, node := range groupMembers {
			if !node.Enabled {
				continue
			}
			if geoip.Active().BlockedNode(node.Server, node.DisplayName) {
				continue
			}
			payload, ok := normalizedNodeMap(node)
			if !ok {
				continue
			}
			name := runtimeNodeName(node)
			memberNames = append(memberNames, name)
			if _, exists := seenProxyNames[name]; exists {
				continue
			}
			payload["name"] = name
			root["proxies"] = append(root["proxies"].([]map[string]any), payload)
			seenProxyNames[name] = struct{}{}
		}
		if len(memberNames) == 0 {
			memberNames = []string{"DIRECT"}
		}
		group := buildProxyGroup(pool, groupName, memberNames, testURL)
		root["proxy-groups"] = append(root["proxy-groups"].([]map[string]any), group)

		// Internal listener on 127.0.0.1 with mixed type, one per pool
		internalPort, err := InternalPort(pool.ID)
		if err != nil {
			return nil, err
		}
		listenerCfg := listener{
			Name:   fmt.Sprintf("pool-%d", pool.ID),
			Type:   "mixed",
			Listen: "127.0.0.1",
			Port:   internalPort,
			Proxy:  groupName,
		}
		if pool.AuthUsername != "" {
			listenerCfg.Users = []map[string]string{{
				"username": pool.AuthUsername,
				"password": pool.AuthPasswordSecret,
			}}
		}
		root["listeners"] = append(root["listeners"].([]listener), listenerCfg)
	}

	return yaml.Marshal(root)
}

// InternalPort returns the internal Mihomo listener port for a pool.
func InternalPort(poolID int64) (int, error) {
	port := 30000 + int(poolID)
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("pool ID %d maps to invalid port %d", poolID, port)
	}
	return port, nil
}

// ScrapeProxyURL is the in-container mixed-port URL crawlers can use to
// reach GitHub when the Docker bridge's WAN path blackholes TLS.
func ScrapeProxyURL(p models.ProxyPool) string {
	port, err := InternalPort(p.ID)
	if err != nil {
		return ""
	}
	u := url.URL{
		Scheme: "socks5",
		Host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
	}
	if p.AuthUsername != "" {
		u.User = url.UserPassword(p.AuthUsername, p.AuthPasswordSecret)
	}
	return u.String()
}

func buildProbeConfig(secret, controller string, probeMixedPort int, logLevel string, inventory []models.RuntimeNode) ([]byte, error) {
	type listener struct {
		Name   string `yaml:"name"`
		Type   string `yaml:"type"`
		Listen string `yaml:"listen"`
		Port   int    `yaml:"port"`
		Proxy  string `yaml:"proxy"`
		UDP    bool   `yaml:"udp,omitempty"`
	}

	root := map[string]any{
		"mode":                "global",
		"log-level":           config.NormalizeLogLevel(logLevel),
		"allow-lan":           false,
		"mixed-port":          probeMixedPort,
		"external-controller": controller,
		"secret":              secret,
		"proxies":             []map[string]any{},
		"proxy-groups":        []map[string]any{},
		"listeners":           []listener{},
		"rules":               []string{"MATCH,GLOBAL"},
	}

	names := make([]string, 0, len(inventory))
	for _, node := range inventory {
		if !node.Enabled {
			continue
		}
		if geoip.Active().BlockedNode(node.Server, node.DisplayName) {
			continue
		}
		name := runtimeNodeName(node)
		payload, ok := normalizedNodeMap(node)
		if !ok {
			continue
		}
		payload["name"] = name
		root["proxies"] = append(root["proxies"].([]map[string]any), payload)
		names = append(names, name)
	}
	sort.Strings(names)
	root["proxy-groups"] = []map[string]any{
		{
			"name":    "GLOBAL",
			"type":    "select",
			"proxies": append(names, "DIRECT"),
		},
	}
	for slotIndex := 0; slotIndex < MaxProbeSpeedSlots; slotIndex++ {
		root["proxy-groups"] = append(root["proxy-groups"].([]map[string]any), map[string]any{
			"name":    probeSpeedSlotGroupName(slotIndex),
			"type":    "select",
			"proxies": append(names, "DIRECT"),
		})
		root["listeners"] = append(root["listeners"].([]listener), listener{
			Name:   fmt.Sprintf("speed-slot-%d", slotIndex+1),
			Type:   "socks",
			Listen: "127.0.0.1",
			Port:   probeSpeedSlotPort(probeMixedPort, slotIndex),
			Proxy:  probeSpeedSlotGroupName(slotIndex),
			UDP:    false,
		})
	}
	return yaml.Marshal(root)
}

func normalizedNodeMap(node models.RuntimeNode) (map[string]any, bool) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(node.NormalizedJSON), &payload); err != nil || len(payload) == 0 {
		payload = map[string]any{
			"type":   node.Protocol,
			"server": node.Server,
			"port":   node.Port,
			"name":   node.DisplayName,
		}
	}
	sanitizeRuntimeNodePayload(node, payload)
	if err := nodes.SanitizeProxyMap(payload); err != nil {
		log.Printf("skip mihomo node %s/%d (%s:%d): %v", node.SourceType, node.SourceNodeID, node.Server, node.Port, err)
		return nil, false
	}
	return payload, true
}

func sanitizeRuntimeNodePayload(node models.RuntimeNode, payload map[string]any) {
	if payload == nil {
		return
	}
	protocol := strings.ToLower(strings.TrimSpace(node.Protocol))
	if rawType, ok := payload["type"].(string); ok {
		rawType = strings.ToLower(strings.TrimSpace(rawType))
		if protocol != "" && rawType != "" && rawType != protocol && isTransportType(rawType) {
			if _, exists := payload["network"]; !exists {
				payload["network"] = rawType
			}
		}
	}
	if node.Protocol != "" {
		payload["type"] = node.Protocol
	}
	if node.Server != "" {
		payload["server"] = node.Server
	}
	if node.Port > 0 {
		payload["port"] = node.Port
	}
	if name := strings.TrimSpace(node.DisplayName); name != "" {
		payload["name"] = name
	}
}

func isTransportType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tcp", "ws", "grpc", "h2", "http", "quic", "kcp", "httpupgrade":
		return true
	default:
		return false
	}
}

func strategyToMihomo(strategy string) (groupType string, lbStrategy string) {
	switch strategy {
	case "lowest_latency":
		return "url-test", ""
	case "failover":
		return "fallback", ""
	case "sticky":
		return "load-balance", "sticky-sessions"
	default:
		return "load-balance", "round-robin"
	}
}

func runtimeNodeName(node models.RuntimeNode) string {
	name := strings.TrimSpace(node.DisplayName)
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "/", "-")
	return fmt.Sprintf("%s-%d-%s", node.SourceType, node.SourceNodeID, name)
}

func poolGroupName(poolID int64) string {
	return fmt.Sprintf("pool-group-%d", poolID)
}

func probeSpeedSlotGroupName(slotIndex int) string {
	return fmt.Sprintf("SPEED_SLOT_%d", slotIndex+1)
}

func probeSpeedSlotPort(probeMixedPort, slotIndex int) int {
	return probeMixedPort + slotIndex + 1
}

func shouldAttachHealthCheck(pool models.ProxyPool) bool {
	switch pool.Strategy {
	case "failover", "lowest_latency":
		return true
	case "sticky", "round_robin", "":
		return pool.FailoverEnabled
	default:
		return pool.FailoverEnabled
	}
}
