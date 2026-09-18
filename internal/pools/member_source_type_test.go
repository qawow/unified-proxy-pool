package pools

import (
	"strings"
	"testing"
)

// UpdateMembers persisted any source_type string; probes then resolved the id
// against the wrong node table (e.g. a typo'd type fell through to
// subscription_nodes).
func TestUpdateMembersRejectsUnknownSourceType(t *testing.T) {
	ctx, _, _, _, poolSvc := newPublishTestService(t)

	pool, err := poolSvc.Create(ctx, UpsertRequest{
		Name:               "demo-pool",
		AuthUsername:       "testuser",
		AuthPasswordSecret: "testpass",
		Strategy:           "round_robin",
		FailoverEnabled:    true,
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("Create pool error = %v", err)
	}

	err = poolSvc.UpdateMembers(ctx, pool.ID, []MemberInput{{
		SourceType:   "bogus_type",
		SourceNodeID: 1,
		Enabled:      true,
	}})
	if err == nil || !strings.Contains(err.Error(), "source_type") {
		t.Fatalf("UpdateMembers(bogus_type) error = %v, want source_type rejection", err)
	}

	// The whitelisted types must still save.
	for _, st := range []string{"manual", "subscription", "free_proxy"} {
		if err := poolSvc.UpdateMembers(ctx, pool.ID, []MemberInput{{
			SourceType:   st,
			SourceNodeID: 1,
			Enabled:      true,
		}}); err != nil {
			t.Fatalf("UpdateMembers(%q) error = %v", st, err)
		}
	}
}
