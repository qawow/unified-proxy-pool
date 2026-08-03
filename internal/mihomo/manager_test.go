package mihomo

import (
	"context"
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
