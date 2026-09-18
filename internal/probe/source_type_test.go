package probe

import (
	"context"
	"testing"
)

// lookupRuntimeNode/setStatus/updateResult fell through to the subscription
// store for ANY non-manual source_type — a free_proxy pool member's id would
// collide with an unrelated subscription_nodes row and write probe results
// onto the wrong node.
func TestProbeRejectsUnknownSourceType(t *testing.T) {
	// Deps stay nil: the whitelist must reject before touching them.
	s := NewService(nil, nil, nil, nil, nil, nil, nil)
	ctx := context.Background()

	if _, err := s.lookupRuntimeNode(ctx, "free_proxy", 1); err == nil {
		t.Fatal("lookupRuntimeNode(free_proxy) error = nil")
	}
	if err := s.setStatus(ctx, "free_proxy", 1, "testing", ""); err == nil {
		t.Fatal("setStatus(free_proxy) error = nil")
	}
	if err := s.updateResult(ctx, task{SourceType: "free_proxy", SourceNodeID: 1}, nil, nil, "available", "", false); err == nil {
		t.Fatal("updateResult(free_proxy) error = nil")
	}
}
