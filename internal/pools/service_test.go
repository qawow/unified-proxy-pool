package pools

import (
	"testing"
)

func TestInternalPort(t *testing.T) {
	port, err := InternalPort(1)
	if err != nil {
		t.Fatalf("InternalPort(1) error = %v", err)
	}
	if port != 30001 {
		t.Fatalf("expected 30001, got %d", port)
	}
	port, err = InternalPort(42)
	if err != nil {
		t.Fatalf("InternalPort(42) error = %v", err)
	}
	if port != 30042 {
		t.Fatalf("expected 30042, got %d", port)
	}
	if _, err := InternalPort(40000); err == nil {
		t.Fatalf("expected invalid port mapping error")
	}
}
