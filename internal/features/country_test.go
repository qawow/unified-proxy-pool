package features

import "testing"

func TestParseEmptyFeatureJSONBlocksCN(t *testing.T) {
	for _, raw := range []string{"", "{}", `{"webhook_url":"x"}`} {
		cfg := Parse(raw)
		if !cfg.CountryEnabled() {
			t.Errorf("Parse(%q): country filter off; CN would leak in", raw)
		}
		if !cfg.ExitCheckEnabled() {
			t.Errorf("Parse(%q): exit check off", raw)
		}
		f := cfg.CountryFilter()
		if !f.Blocks("CN") || !f.Blocks("中国") {
			t.Errorf("Parse(%q): CN not blocked, blocked=%v", raw, f.Blocked)
		}
		if f.Blocks("HK") || f.Blocks("TW") || f.Blocks("MO") {
			t.Errorf("Parse(%q): HK/TW/MO must stay, blocked=%v", raw, f.Blocked)
		}
	}
}

func TestEmptyBlockedCountriesAllowsCN(t *testing.T) {
	cfg := Parse(`{"country_filter_enabled":true,"blocked_countries":[]}`)
	if cfg.CountryFilter().Blocks("CN") {
		t.Fatal("explicit empty deny list must allow CN")
	}
}

func TestCountryFilterCanBeDisabled(t *testing.T) {
	cfg := Parse(`{"country_filter_enabled":false,"blocked_countries":["CN"]}`)
	if cfg.CountryEnabled() {
		t.Fatal("enabled=false must turn the filter off")
	}
	if cfg.CountryFilter().Blocks("CN") {
		t.Fatal("disabled filter still blocked CN")
	}
}

func TestBlockedCountriesNormalizesSpellings(t *testing.T) {
	cfg := Parse(`{"blocked_countries":["china","CN","中国","hk"]}`)
	f := cfg.CountryFilter()
	if !f.Blocks("CN") || !f.Blocks("HK") {
		t.Fatalf("blocked=%v", f.Blocked)
	}
	if len(f.Blocked) != 2 {
		t.Fatalf("dedupe failed: %v", f.Blocked)
	}
}
