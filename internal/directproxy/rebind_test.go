package directproxy

import (
	"context"
	"net"
	"testing"
	"time"

	"unified-proxy-pool/internal/freproxies"
)

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func dialCheck(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func waitFor(t *testing.T, wantErr bool, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := dialCheck(addr)
		if (err == nil) == !wantErr {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if wantErr {
		t.Fatalf("%s is still listening", addr)
	}
	t.Fatalf("%s never started listening", addr)
}

// SetChainOptions used to mutate cfg.ChainAddr/ChainEnabled without touching
// the live listener: the UI showed the new address while traffic still
// arrived on the old one — or nowhere after an enable toggle.
func TestSetChainOptionsRebindsListener(t *testing.T) {
	addrA := freeTCPAddr(t)
	addrB := freeTCPAddr(t)

	free := freproxies.NewService(freproxies.NewMemoryStore(), nil, nil, false)
	s := New(Config{ChainEnabled: true, ChainAddr: addrA}, free)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, false, addrA)

	// Changing listen_addr must move the listener.
	s.SetChainOptions(ChainOptions{Enabled: true, ListenAddr: addrB, Hops: 2})
	waitFor(t, false, addrB)
	waitFor(t, true, addrA)

	// Disabling must actually close it.
	s.SetChainOptions(ChainOptions{Enabled: false, ListenAddr: addrB, Hops: 2})
	waitFor(t, true, addrB)
}

// The same bug hid enabled=false → enabled=true: nothing ever opened the
// listener before restart.
func TestSetChainOptionsBindsOnEnable(t *testing.T) {
	addr := freeTCPAddr(t)

	free := freproxies.NewService(freproxies.NewMemoryStore(), nil, nil, false)
	s := New(Config{ChainEnabled: false, ChainAddr: addr}, free)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dialCheck(addr); err == nil {
		t.Fatal("chain listener should not be bound while disabled")
	}

	s.SetChainOptions(ChainOptions{Enabled: true, ListenAddr: addr, Hops: 2})
	waitFor(t, false, addr)
}
