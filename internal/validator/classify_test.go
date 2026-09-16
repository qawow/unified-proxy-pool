package validator

import (
	"errors"
	"fmt"
	"testing"
)

// TestClassifyValidateErr covers the classes the panel's diagnosis depends on.
// The whole point of carrying the probe error was that a batch failing 500/500
// becomes interpretable: all-timeouts means the network, all-refused means the
// proxies are gone, all-tls means something is intercepting the probes.
func TestClassifyValidateErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"timeout", errors.New("Get https://gstatic: dial tcp: i/o timeout"), "timeout"},
		{"deadline", errors.New("context deadline exceeded"), "timeout"},
		{"refused", errors.New("dial tcp 1.2.3.4:8080: connect: connection refused"), "connect"},
		{"no route", errors.New("no route to host"), "connect"},
		{"tls handshake", errors.New("tls: handshake failure"), "tls"},
		{"certificate", errors.New("x509: certificate signed by unknown authority"), "tls"},
		{"blocked country", errors.New("blocked country: CN"), "blocked_country"},
		{"aborted", errors.New("validation aborted"), "aborted"},
		{"plain failure", errors.New("something else entirely"), "fail"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyValidateErr(c.err); got != c.want {
				t.Fatalf("classifyValidateErr(%q) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

// TestClassifyValidateErrChained: the failure is now wrapped as
// "proxy validation failed: <cause>" and must still classify by the cause —
// the prefix must not drown it out and send everything to "fail".
func TestClassifyValidateErrChained(t *testing.T) {
	cause := errors.New("dial tcp: i/o timeout")
	wrapped := fmt.Errorf("proxy validation failed: %w", cause)
	if got := classifyValidateErr(wrapped); got != "timeout" {
		t.Fatalf("wrapped timeout classified as %q, want timeout", got)
	}

	cause2 := errors.New("connect: connection refused")
	wrapped2 := fmt.Errorf("proxy validation failed: %w", cause2)
	if got := classifyValidateErr(wrapped2); got != "connect" {
		t.Fatalf("wrapped refused classified as %q, want connect", got)
	}
}

// TestClassifyValidateErrNil must never hand back a bogus bucket.
func TestClassifyValidateErrNil(t *testing.T) {
	if got := classifyValidateErr(nil); got != "" {
		t.Fatalf("classifyValidateErr(nil) = %q, want empty", got)
	}
}
