package conntrack

import "testing"

func TestBeginEndRoundTrip(t *testing.T) {
	tr := New()
	id := tr.Begin("single", "203.0.113.9")
	if id == 0 {
		t.Fatal("Begin returned id 0")
	}
	tr.SetUpstream(id, "10.0.0.1:8080")
	list := tr.List()
	if len(list) != 1 {
		t.Fatalf("live = %d, want 1", len(list))
	}
	if list[0].ClientIP != "203.0.113.9" || list[0].Upstream != "10.0.0.1:8080" || list[0].Channel != "single" {
		t.Errorf("live conn = %+v", list[0])
	}
	tr.End(id, 0, 0)
	if got := len(tr.List()); got != 0 {
		t.Errorf("after End, live = %d", got)
	}
}

func TestEndUnknownIDIsNoOp(t *testing.T) {
	tr := New()
	tr.End(99, 0, 0)
	if len(tr.List()) != 0 {
		t.Error("End of unknown id created state")
	}
}
