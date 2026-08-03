package mihomo

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallerLatestReleasePrefersDefaultAsset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]any{
			"tag_name": "v1.19.23",
			"name":     "v1.19.23",
			"assets": []map[string]any{
				{"name": "mihomo-linux-amd64-compatible-v1.19.23.gz", "browser_download_url": "https://example.invalid/compatible", "size": 1},
				{"name": "mihomo-linux-amd64-v1.19.23.gz", "browser_download_url": "https://example.invalid/default", "size": 1},
				{"name": "mihomo-linux-amd64-v3-v1.19.23.gz", "browser_download_url": "https://example.invalid/v3", "size": 1},
				{"name": "mihomo-linux-amd64-v1-go123-v1.19.23.gz", "browser_download_url": "https://example.invalid/go123", "size": 1},
				{"name": "mihomo-windows-amd64-v1.19.23.zip", "browser_download_url": "https://example.invalid/windows", "size": 1},
			},
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer server.Close()

	tempDir := t.TempDir()
	installer := NewInstallerWithOptions(InstallerOptions{
		HTTPClient:    server.Client(),
		ReleaseAPIURL: server.URL,
		InstallDir:    filepath.Join(tempDir, "bin"),
		SelectionPath: filepath.Join(tempDir, "mihomo-binary.txt"),
		HostOS:        "linux",
		HostArch:      "amd64",
	})

	release, err := installer.LatestRelease(context.Background())
	if err != nil {
		t.Fatalf("LatestRelease() error = %v", err)
	}
	if len(release.Assets) != 4 {
		t.Fatalf("LatestRelease() returned %d assets, want 4", len(release.Assets))
	}
	if !release.Assets[0].Recommended {
		t.Fatalf("expected first asset to be recommended")
	}
	if got := release.Assets[0].Name; got != "mihomo-linux-amd64-v1.19.23.gz" {
		t.Fatalf("recommended asset = %q", got)
	}
}

func TestInstallerInstallExtractsGzipAsset(t *testing.T) {
	const assetName = "mihomo-linux-amd64-v1.19.23.gz"
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	if _, err := gzipWriter.Write([]byte("linux-binary")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	baseURL := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			payload := map[string]any{
				"tag_name": "v1.19.23",
				"name":     "v1.19.23",
				"assets": []map[string]any{
					{"name": assetName, "browser_download_url": baseURL + "/assets/" + assetName, "size": archive.Len()},
				},
			}
			_ = json.NewEncoder(w).Encode(payload)
		case "/assets/" + assetName:
			_, _ = w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	baseURL = server.URL

	tempDir := t.TempDir()
	installDir := filepath.Join(tempDir, "bin")
	selectionPath := filepath.Join(tempDir, "mihomo-binary.txt")
	installer := NewInstallerWithOptions(InstallerOptions{
		HTTPClient:    server.Client(),
		ReleaseAPIURL: server.URL + "/latest",
		InstallDir:    installDir,
		SelectionPath: selectionPath,
		HostOS:        "linux",
		HostArch:      "amd64",
	})

	result, err := installer.Install(context.Background(), "")
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if result.InstalledPath == "" {
		t.Fatalf("Install() did not return an installed path")
	}
	data, err := os.ReadFile(result.InstalledPath)
	if err != nil {
		t.Fatalf("ReadFile(installed) error = %v", err)
	}
	if string(data) != "linux-binary" {
		t.Fatalf("installed binary content = %q", string(data))
	}
	selectedPath, err := os.ReadFile(selectionPath)
	if err != nil {
		t.Fatalf("ReadFile(selection) error = %v", err)
	}
	if string(selectedPath) != result.InstalledPath {
		t.Fatalf("selection path = %q, want %q", string(selectedPath), result.InstalledPath)
	}
}

func TestInstallerInstallExtractsZipAsset(t *testing.T) {
	const assetName = "mihomo-windows-amd64-v1.19.23.zip"
	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	entry, err := zipWriter.Create("mihomo.exe")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := entry.Write([]byte("windows-binary")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	baseURL := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			payload := map[string]any{
				"tag_name": "v1.19.23",
				"name":     "v1.19.23",
				"assets": []map[string]any{
					{"name": assetName, "browser_download_url": baseURL + "/assets/" + assetName, "size": archive.Len()},
				},
			}
			_ = json.NewEncoder(w).Encode(payload)
		case "/assets/" + assetName:
			_, _ = w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	baseURL = server.URL

	tempDir := t.TempDir()
	installer := NewInstallerWithOptions(InstallerOptions{
		HTTPClient:    server.Client(),
		ReleaseAPIURL: server.URL + "/latest",
		InstallDir:    filepath.Join(tempDir, "bin"),
		SelectionPath: filepath.Join(tempDir, "mihomo-binary.txt"),
		HostOS:        "windows",
		HostArch:      "amd64",
	})

	result, err := installer.Install(context.Background(), "")
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if filepath.Ext(result.InstalledPath) != ".exe" {
		t.Fatalf("installed path = %q, want .exe suffix", result.InstalledPath)
	}
	data, err := os.ReadFile(result.InstalledPath)
	if err != nil {
		t.Fatalf("ReadFile(installed) error = %v", err)
	}
	if string(data) != "windows-binary" {
		t.Fatalf("installed binary content = %q", string(data))
	}
}
