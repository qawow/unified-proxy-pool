package freproxies

import (
	"context"
	"testing"

	"unified-proxy-pool/internal/crawlers"
)

func TestRecordValidateYieldSkipsTinySamples(t *testing.T) {
	svc := NewService(NewMemoryStore(), crawlers.NewRegistry(nil), nil, false)
	ctx := context.Background()
	svc.RecordValidateYield(ctx, map[string][2]int{
		"tiny": {0, 3},
		"big":  {2, 23},
	})
	tiny, err := svc.store.ListSourceYield(ctx, "tiny", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tiny) != 0 {
		t.Fatalf("tiny sample persisted: %+v", tiny)
	}
	big, err := svc.store.ListSourceYield(ctx, "big", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(big) != 1 || big[0].Sampled != 25 || big[0].Alive != 2 {
		t.Fatalf("big sample: %+v", big)
	}
}
