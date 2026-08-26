package aisvc

import "strings"

// Effort is the portable thinking-depth knob most chat APIs now share:
// OpenAI `reasoning_effort`, DeepSeek `reasoning_effort`, Gemini
// `thinkingConfig.thinkingBudget` (mapped by the gateway), Claude via
// OpenAI-compatible proxies. Values: off, low, medium, high, max.
const (
	EffortOff    = "off"
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
	EffortMax    = "max"
)

// NormalizeEffort accepts the five names, common aliases, and the old 0–10
// slider so existing clients keep working.
func NormalizeEffort(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case EffortOff, EffortLow, EffortMedium, EffortHigh, EffortMax:
		return s
	case "none", "disable", "disabled", "0":
		return EffortOff
	case "1", "2", "3", "min", "minimal":
		return EffortLow
	case "4", "5", "6", "mid", "default", "":
		return EffortMedium
	case "7", "8", "9":
		return EffortHigh
	case "10", "ultra", "xhigh", "highest":
		return EffortMax
	default:
		return EffortMedium
	}
}

// effortParams turns a portable effort into sampling + the vendor field.
//
// Temperature still drops as effort rises (more deterministic extraction),
// and max_tokens grows so a high/max call has room for a long reasoning
// trace plus the actual JSON. ReasoningEffort is omitted for "off" so
// gateways that reject the field on non-reasoning models stay happy.
type effortParams struct {
	Temperature     float64
	MaxTokens       int
	ReasoningEffort string // empty when off
}

func paramsForEffort(effort string) effortParams {
	switch NormalizeEffort(effort) {
	case EffortOff:
		return effortParams{Temperature: 0.4, MaxTokens: 1200}
	case EffortLow:
		return effortParams{Temperature: 0.35, MaxTokens: 2000, ReasoningEffort: EffortLow}
	case EffortHigh:
		return effortParams{Temperature: 0.2, MaxTokens: 4800, ReasoningEffort: EffortHigh}
	case EffortMax:
		return effortParams{Temperature: 0.15, MaxTokens: 8000, ReasoningEffort: EffortMax}
	default: // medium
		return effortParams{Temperature: 0.3, MaxTokens: 3200, ReasoningEffort: EffortMedium}
	}
}
