package cfscan

import "testing"

func TestParseTargetsIPv4AndCIDR(t *testing.T) {
	got, err := ParseTargets("1.2.3.4\n# skip\n10.0.0.0/30\n1.2.3.4\n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"1.2.3.4": true, "10.0.0.0": true, "10.0.0.1": true, "10.0.0.2": true, "10.0.0.3": true}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d (%v)", len(got), len(want), got)
	}
	for _, ip := range got {
		if !want[ip] {
			t.Errorf("unexpected %s", ip)
		}
	}
}

func TestParseTargetsRejectsHugeCIDR(t *testing.T) {
	_, err := ParseTargets("10.0.0.0/8")
	if err == nil {
		t.Fatal("expected size error")
	}
}

func TestParseTargetsRejectsIPv6(t *testing.T) {
	_, err := ParseTargets("2001:db8::1")
	if err == nil {
		t.Fatal("expected ipv6 error")
	}
}

func TestIsCFTrace(t *testing.T) {
	body := []byte("fl=123f\ncolo=SJC\nhip=1.1.1.1\n")
	if !isCFTrace(body) {
		t.Fatal("should match")
	}
	if isCFTrace([]byte("hello nginx")) {
		t.Fatal("false positive")
	}
	if traceField(body, "colo") != "SJC" {
		t.Fatalf("colo=%s", traceField(body, "colo"))
	}
}
