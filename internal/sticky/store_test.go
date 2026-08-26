package sticky

import (
	"testing"
	"time"
)

func TestGetProxyRemembersProtocol(t *testing.T) {
	s := New(time.Minute)
	s.PutProxy("1.2.3.4", "10.0.0.1:1080", "socks5")
	addr, proto, ok := s.GetProxy("1.2.3.4")
	if !ok {
		t.Fatal("sticky miss right after Put")
	}
	if addr != "10.0.0.1:1080" || proto != "socks5" {
		t.Errorf("got %s/%s, want 10.0.0.1:1080/socks5", addr, proto)
	}
	// The old Get still works so existing callers keep compiling.
	if got, ok := s.Get("1.2.3.4"); !ok || got != addr {
		t.Errorf("Get = %q, %v", got, ok)
	}
}

func TestExpiredStickyIsAMiss(t *testing.T) {
	s := New(time.Millisecond)
	s.PutProxy("1.2.3.4", "10.0.0.1:80", "http")
	time.Sleep(5 * time.Millisecond)
	if _, _, ok := s.GetProxy("1.2.3.4"); ok {
		t.Error("expired sticky entry was still returned")
	}
}

func TestEmptyClientIPIsIgnored(t *testing.T) {
	s := New(time.Minute)
	s.PutProxy("", "10.0.0.1:80", "http")
	if _, _, ok := s.GetProxy(""); ok {
		t.Error("empty client IP must never stick — that is how the feature used to be silently dead")
	}
}
