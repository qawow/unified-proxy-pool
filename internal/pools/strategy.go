package pools

import (
	"encoding/json"
	"strings"

	"unified-proxy-pool/internal/models"
)

// StrategyTemplate describes a built-in advanced preset users can apply.
type StrategyTemplate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Optional defaults applied into StrategyAdvanced when selected.
	Defaults models.StrategyAdvanced `json:"defaults"`
}

func StrategyTemplates() []StrategyTemplate {
	lazyTrue := true
	lazyFalse := false
	return []StrategyTemplate{
		{
			ID:          "",
			Name:        "跟随基础策略",
			Description: "仅使用四种基础调度，不额外覆盖 Mihomo 参数。",
		},
		{
			ID:          "fast_test",
			Name:        "快速测速",
			Description: "url-test + 较短探测间隔，适合追求低延迟。",
			Defaults: models.StrategyAdvanced{
				DisplayName: "快速测速",
				Template:    "fast_test",
				GroupType:   "url-test",
				Interval:    60,
				Tolerance:   50,
				Lazy:        &lazyFalse,
			},
		},
		{
			ID:          "stable",
			Name:        "稳定优先",
			Description: "fallback 故障转移 + 健康检查，优先保证可用性。",
			Defaults: models.StrategyAdvanced{
				DisplayName: "稳定优先",
				Template:    "stable",
				GroupType:   "fallback",
				Interval:    300,
				Lazy:        &lazyTrue,
			},
		},
		{
			ID:          "hash_sticky",
			Name:        "哈希粘滞",
			Description: "load-balance + consistent-hashing，同目标更稳定落在同一节点。",
			Defaults: models.StrategyAdvanced{
				DisplayName: "哈希粘滞",
				Template:    "hash_sticky",
				GroupType:   "load-balance",
				LBStrategy:  "consistent-hashing",
				Interval:    300,
				Lazy:        &lazyTrue,
			},
		},
		{
			ID:          "manual_select",
			Name:        "手动选择",
			Description: "select 组，由控制器/面板手动指定当前节点。",
			Defaults: models.StrategyAdvanced{
				DisplayName:   "手动选择",
				Template:      "manual_select",
				GroupType:     "select",
				DisableHealth: true,
			},
		},
		{
			ID:          "custom",
			Name:        "自定义",
			Description: "自行填写 group 类型、探测参数或 extra JSON 字段。",
			Defaults: models.StrategyAdvanced{
				DisplayName: "自定义策略",
				Template:    "custom",
			},
		},
	}
}

func ParseStrategyAdvanced(raw string) models.StrategyAdvanced {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return models.StrategyAdvanced{}
	}
	var adv models.StrategyAdvanced
	if err := json.Unmarshal([]byte(raw), &adv); err != nil {
		return models.StrategyAdvanced{}
	}
	return adv
}

func EncodeStrategyAdvanced(adv models.StrategyAdvanced) string {
	if adv.DisplayName == "" && adv.Template == "" && adv.GroupType == "" &&
		adv.LBStrategy == "" && adv.HealthURL == "" && adv.Interval == 0 &&
		adv.Tolerance == 0 && adv.Lazy == nil && !adv.DisableHealth && len(adv.Extra) == 0 {
		return "{}"
	}
	b, err := json.Marshal(adv)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func applyTemplateDefaults(adv models.StrategyAdvanced) models.StrategyAdvanced {
	if adv.Template == "" || adv.Template == "custom" {
		return adv
	}
	for _, t := range StrategyTemplates() {
		if t.ID != adv.Template {
			continue
		}
		def := t.Defaults
		if adv.DisplayName == "" {
			adv.DisplayName = def.DisplayName
		}
		if adv.GroupType == "" {
			adv.GroupType = def.GroupType
		}
		if adv.LBStrategy == "" {
			adv.LBStrategy = def.LBStrategy
		}
		if adv.HealthURL == "" {
			adv.HealthURL = def.HealthURL
		}
		if adv.Interval == 0 {
			adv.Interval = def.Interval
		}
		if adv.Tolerance == 0 {
			adv.Tolerance = def.Tolerance
		}
		if adv.Lazy == nil {
			adv.Lazy = def.Lazy
		}
		if !adv.DisableHealth {
			adv.DisableHealth = def.DisableHealth
		}
		break
	}
	return adv
}

// buildProxyGroup constructs Mihomo proxy-group from base strategy + advanced overrides.
func buildProxyGroup(pool models.ProxyPool, groupName string, memberNames []string, defaultTestURL string) map[string]any {
	adv := applyTemplateDefaults(ParseStrategyAdvanced(pool.StrategyAdvancedJSON))
	if pool.StrategyLabel != "" && adv.DisplayName == "" {
		adv.DisplayName = pool.StrategyLabel
	}

	groupType, lbStrategy := strategyToMihomo(pool.Strategy)
	if adv.GroupType != "" {
		groupType = strings.TrimSpace(adv.GroupType)
	}
	if adv.LBStrategy != "" {
		lbStrategy = strings.TrimSpace(adv.LBStrategy)
	}

	group := map[string]any{
		"name":    groupName,
		"type":    groupType,
		"proxies": memberNames,
	}
	if lbStrategy != "" && groupType == "load-balance" {
		group["strategy"] = lbStrategy
	}

	attachHealth := shouldAttachHealthCheck(pool) && !adv.DisableHealth
	if attachHealth {
		url := defaultTestURL
		if adv.HealthURL != "" {
			url = adv.HealthURL
		}
		interval := 300
		if adv.Interval > 0 {
			interval = adv.Interval
		}
		group["url"] = url
		group["interval"] = interval
		lazy := true
		if adv.Lazy != nil {
			lazy = *adv.Lazy
		}
		group["lazy"] = lazy
		if adv.Tolerance > 0 {
			group["tolerance"] = adv.Tolerance
		}
	} else if !adv.DisableHealth && (groupType == "url-test" || groupType == "fallback") {
		// custom group type that still needs health fields
		url := defaultTestURL
		if adv.HealthURL != "" {
			url = adv.HealthURL
		}
		interval := 300
		if adv.Interval > 0 {
			interval = adv.Interval
		}
		group["url"] = url
		group["interval"] = interval
		if adv.Tolerance > 0 {
			group["tolerance"] = adv.Tolerance
		}
		if adv.Lazy != nil {
			group["lazy"] = *adv.Lazy
		}
	}

	for k, v := range adv.Extra {
		if k == "" || k == "name" || k == "proxies" {
			continue
		}
		group[k] = v
	}
	return group
}

func strategyDisplayName(pool models.ProxyPool) string {
	if strings.TrimSpace(pool.StrategyLabel) != "" {
		return strings.TrimSpace(pool.StrategyLabel)
	}
	adv := ParseStrategyAdvanced(pool.StrategyAdvancedJSON)
	if strings.TrimSpace(adv.DisplayName) != "" {
		return strings.TrimSpace(adv.DisplayName)
	}
	switch defaultStrategy(pool.Strategy) {
	case "lowest_latency":
		return "最低延迟"
	case "failover":
		return "故障转移"
	case "sticky":
		return "会话粘滞"
	default:
		return "轮询调度"
	}
}
