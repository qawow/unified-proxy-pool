package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"unified-proxy-pool/internal/version"
)

func TestIsNewer(t *testing.T) {
	if !isNewer("dev", "abc1234") {
		t.Fatal("dev should update")
	}
	if isNewer("abc1234dead", "abc1234") {
		t.Fatal("prefix match is same commit")
	}
	if isNewer("abc1234", "abc1234") {
		t.Fatal("equal")
	}
	if !isNewer("aaa1111", "bbb2222") {
		t.Fatal("different sha is newer")
	}
}

func TestCheckNewer(t *testing.T) {
	version.Commit = "aaa1111"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "version.txt") {
			_, _ = w.Write([]byte("bbb2222deadbeef\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()
	s := New("owner/repo", "nightly", ts.Client(), nil)
	s.baseURL = ts.URL
	st, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.RemoteShort != "bbb2222" {
		t.Fatalf("remote short = %s", st.RemoteShort)
	}
	if !st.Newer {
		t.Fatal("expected newer")
	}
}

func TestValidateBinary(t *testing.T) {
	if err := validateBinary([]byte("tiny"), ""); err == nil {
		t.Fatal("tiny must fail")
	}
	body := make([]byte, minBinaryBytes+4)
	copy(body, []byte(elfMagic))
	sum := sha256.Sum256(body)
	good := hex.EncodeToString(sum[:])
	if err := validateBinary(body, good); err != nil {
		t.Fatal(err)
	}
	if err := validateBinary(body, ""); err == nil {
		t.Fatal("missing checksum must fail")
	}
	if err := validateBinary(body, strings.Repeat("a", 64)); err == nil {
		t.Fatal("checksum mismatch must fail")
	}
}

func TestParseChecksum(t *testing.T) {
	want := strings.Repeat("ab", 32)
	if got := parseChecksum("# comment\n" + want + "  unified-proxy-pool\n"); got != want {
		t.Fatalf("parseChecksum = %q", got)
	}
	if got := parseChecksum("not a checksum\n"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// A tampered binary from the untrusted mirror must be rejected even though it
// is a valid, large ELF file — the checksum comes from the verified channel.
func TestPrepareRejectsTamperedBinary(t *testing.T) {
	version.Commit = "aaa1111"
	good := make([]byte, minBinaryBytes+16)
	copy(good, []byte(elfMagic))
	sum := sha256.Sum256(good)
	tampered := make([]byte, len(good))
	copy(tampered, good)
	tampered[len(tampered)-1] ^= 0xff

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "version.txt"):
			_, _ = w.Write([]byte("bbb2222deadbeef\n"))
		case strings.HasSuffix(r.URL.Path, ".sha256"):
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  unified-proxy-pool\n"))
		default:
			_, _ = w.Write(tampered)
		}
	}))
	defer ts.Close()
	s := New("owner/repo", "nightly", ts.Client(), nil)
	s.baseURL = ts.URL
	if _, err := s.Prepare(context.Background()); err == nil {
		t.Fatal("expected checksum mismatch, got nil")
	} else if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// No checksum published → refuse rather than install unverified bytes.
func TestPrepareRefusesWithoutChecksum(t *testing.T) {
	version.Commit = "aaa1111"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "version.txt") {
			_, _ = w.Write([]byte("bbb2222deadbeef\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()
	s := New("owner/repo", "nightly", ts.Client(), nil)
	s.baseURL = ts.URL
	if _, err := s.Prepare(context.Background()); err == nil {
		t.Fatal("expected refusal without checksum")
	} else if !strings.Contains(err.Error(), "checksum unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteReplace(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "upp")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, ".unified-proxy-pool.new")
	payload := make([]byte, minBinaryBytes+8)
	copy(payload, []byte(elfMagic))
	if err := os.WriteFile(tmp, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(payload) {
		t.Fatalf("len %d", len(got))
	}
}

func TestDoGetError(t *testing.T) {
	s := New("", "", &http.Client{Timeout: 50 * time.Millisecond}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := s.doGet(ctx, s.client, "http://127.0.0.1:1/nope")
	if err == nil {
		t.Fatal("want error")
	}
}
