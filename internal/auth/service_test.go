package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("admin")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if !VerifyPassword(hash, "admin") {
		t.Fatalf("VerifyPassword() should accept the original password")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatalf("VerifyPassword() should reject the wrong password")
	}
}
