package geoip

import "testing"

func TestNormalizeMapsChineseAndISO(t *testing.T) {
	cases := map[string]string{
		"CN": "CN", "cn": "CN", "China": "CN", "中国": "CN", "大陆": "CN",
		"HK": "HK", "香港": "HK", "Hong Kong": "HK",
		"TW": "TW", "台湾": "TW", "台灣": "TW",
		"MO": "MO", "澳门": "MO",
		"US": "US", "us": "US",
		"": "", "CN2": "", "not-a-country": "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGuessFromLabelDoesNotTreatCN2AsChina(t *testing.T) {
	if got := GuessFromLabel("美国 CN2 GIA"); got != "" {
		t.Errorf("CN2 product name guessed %q, want empty", got)
	}
	if got := GuessFromLabel("中国 上海"); got != "CN" {
		t.Errorf("中国 上海 = %q, want CN", got)
	}
	if got := GuessFromLabel("中国香港"); got != "HK" {
		t.Errorf("中国香港 = %q, want HK (checked before CN)", got)
	}
	if got := GuessFromLabel("台湾 01"); got != "TW" {
		t.Errorf("台湾 = %q, want TW", got)
	}
}

func TestDefaultFilterBlocksCNKeepsHK(t *testing.T) {
	f := DefaultFilter()
	if !f.Blocks("CN") || !f.Blocks("中国") || !f.Blocks("china") {
		t.Fatal("default filter must block mainland China under every spelling")
	}
	if f.Blocks("HK") || f.Blocks("香港") || f.Blocks("TW") || f.Blocks("MO") || f.Blocks("US") {
		t.Fatal("default filter must not block HK/TW/MO/US")
	}
	if f.Blocks("") {
		t.Fatal("unknown region must not be blocked")
	}
}

func TestBlockedNodeUsesNameAndCNHost(t *testing.T) {
	f := DefaultFilter()
	if !f.BlockedNode("1.2.3.4", "中国 上海") {
		t.Fatal("display name 中国 must drop the node")
	}
	if !f.BlockedNode("www.example.cn", "whatever") {
		t.Fatal(".cn host must drop the node")
	}
	if f.BlockedNode("1.2.3.4", "美国 CN2") {
		t.Fatal("US CN2 node must stay")
	}
	if f.BlockedNode("node.example.hk", "香港") {
		t.Fatal("HK must stay under the default filter")
	}
}

func TestEmptyBlockedListBlocksNothing(t *testing.T) {
	f := newFilter(true, true, nil)
	if f.Blocks("CN") {
		t.Fatal("empty deny list with Enabled=true must allow CN")
	}
}

func TestDisabledFilterBlocksNothing(t *testing.T) {
	f := newFilter(false, true, []string{"CN"})
	if f.Blocks("CN") {
		t.Fatal("disabled filter must allow CN")
	}
}

func TestSetFilterIsVisibleToActive(t *testing.T) {
	prev := Active()
	t.Cleanup(func() { SetFilter(prev) })
	SetFilter(newFilter(true, true, []string{"RU"}))
	if !Active().Blocks("RU") || Active().Blocks("CN") {
		t.Fatalf("Active after SetFilter = %+v", Active())
	}
}
