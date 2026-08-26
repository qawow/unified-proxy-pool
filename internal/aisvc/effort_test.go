package aisvc

import "testing"

func TestNormalizeEffortNamesAndLegacyNumbers(t *testing.T) {
	cases := map[string]string{
		"off": EffortOff, "OFF": EffortOff, "0": EffortOff, "none": EffortOff,
		"low": EffortLow, "1": EffortLow, "minimal": EffortLow,
		"medium": EffortMedium, "5": EffortMedium, "": EffortMedium, "mid": EffortMedium,
		"high": EffortHigh, "8": EffortHigh,
		"max": EffortMax, "10": EffortMax, "ultra": EffortMax,
		"bogus": EffortMedium,
	}
	for in, want := range cases {
		if got := NormalizeEffort(in); got != want {
			t.Errorf("NormalizeEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParamsForEffortOmitsFieldWhenOff(t *testing.T) {
	off := paramsForEffort(EffortOff)
	if off.ReasoningEffort != "" {
		t.Errorf("off still sends reasoning_effort=%q; gateways reject that on non-reasoning models", off.ReasoningEffort)
	}
	max := paramsForEffort(EffortMax)
	if max.ReasoningEffort != EffortMax {
		t.Errorf("max ReasoningEffort = %q", max.ReasoningEffort)
	}
	if max.MaxTokens <= off.MaxTokens {
		t.Errorf("max tokens %d should exceed off %d", max.MaxTokens, off.MaxTokens)
	}
	if max.Temperature >= off.Temperature {
		t.Errorf("max should be cooler than off: %v vs %v", max.Temperature, off.Temperature)
	}
}
