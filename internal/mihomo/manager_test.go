package mihomo

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestApplyConfigBundleRotatesControllerSecret(t *testing.T) {
	const oldSecret = "old-secret"
	const newSecret = "new-secret"

	newController := func(t *testing.T) *httptest.Server {
		t.Helper()

		var mu sync.Mutex
		expectedSecret := oldSecret
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()

			wantAuth := "Bearer " + expectedSecret
			if got := r.Header.Get("Authorization"); got != wantAuth {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			switch r.URL.Path {
			case "/version":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"version":"test"}`))
			case "/configs":
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("ReadAll() error = %v", err)
				}
				if !strings.Contains(string(body), newSecret) {
					t.Fatalf("config payload should contain next secret %q, got %s", newSecret, string(body))
				}
				expectedSecret = newSecret
				w.WriteHeader(http.StatusNoContent)
			default:
				http.NotFound(w, r)
			}
		}))
	}

	prodServer := newController(t)
	defer prodServer.Close()
	probeServer := newController(t)
	defer probeServer.Close()

	tempDir := t.TempDir()
	prodPath := filepath.Join(tempDir, "mihomo-prod.yaml")
	probePath := filepath.Join(tempDir, "mihomo-probe.yaml")

	manager := &Manager{
		opts: Options{
			ProdConfigPath:      prodPath,
			ProbeConfigPath:     probePath,
			ProdControllerAddr:  strings.TrimPrefix(prodServer.URL, "http://"),
			ProbeControllerAddr: strings.TrimPrefix(probeServer.URL, "http://"),
		},
		httpClient:   &http.Client{Timeout: 2 * time.Second},
		binaryPath:   "/tmp/mihomo",
		hasBinary:    true,
		lastSecret:   oldSecret,
		expectedExit: make(map[int]struct{}),
	}

	prodPayload := []byte("secret: \"" + newSecret + "\"\nmode: rule\n")
	probePayload := []byte("secret: \"" + newSecret + "\"\nmode: global\n")

	if err := manager.ApplyConfigBundle(context.Background(), prodPayload, probePayload, newSecret); err != nil {
		t.Fatalf("ApplyConfigBundle() error = %v", err)
	}
	if got := manager.currentSecret(); got != newSecret {
		t.Fatalf("currentSecret() = %q, want %q", got, newSecret)
	}

	prodFile, err := os.ReadFile(prodPath)
	if err != nil {
		t.Fatalf("ReadFile(prod) error = %v", err)
	}
	if string(prodFile) != string(prodPayload) {
		t.Fatalf("unexpected prod config payload: %s", string(prodFile))
	}

	probeFile, err := os.ReadFile(probePath)
	if err != nil {
		t.Fatalf("ReadFile(probe) error = %v", err)
	}
	if string(probeFile) != string(probePayload) {
		t.Fatalf("unexpected probe config payload: %s", string(probeFile))
	}
}

// A config mihomo refuses must leave the previous file in place: restarting
// from a rejected config is what turned one bad subscription node into a
// crash loop that took every pool listener down.
func TestApplySingleConfigKeepsLastGoodOnRejection(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "prod.yaml")
	if err := os.WriteFile(cfgPath, []byte("good: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"proxy 3: obfs mode error"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "http://")

	m := NewManager(Options{
		BinaryPath:         "/nonexistent/mihomo",
		RuntimeDir:         dir,
		ProdConfigPath:     cfgPath,
		ProdControllerAddr: addr,
	})
	m.mu.Lock()
	m.hasBinary = true
	m.mu.Unlock()

	err := m.applySingleConfig(context.Background(), "prod", cfgPath, []byte("bad: 2\n"))
	if err == nil {
		t.Fatal("expected rejection error")
	}
	if !errors.Is(err, ErrConfigRejected) {
		t.Fatalf("error = %v, want ErrConfigRejected", err)
	}
	got, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "good: 1\n" {
		t.Fatalf("config on disk = %q, want the previous good config", string(got))
	}
}

// Concurrent publishes used to collide on a fixed "<path>.tmp".
func TestWriteFileAtomicConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := writeFileAtomic(path, []byte(strings.Repeat("x", 1024))); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600 (config holds the controller secret)", info.Mode().Perm())
	}
	// No leftover temp files.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries, want 1", len(entries))
	}
}
