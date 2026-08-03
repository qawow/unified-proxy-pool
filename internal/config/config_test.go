package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveMihomoBinaryPrefersOverride(t *testing.T) {
	got := resolveMihomoBinary(t.TempDir(), t.TempDir(), "/custom/mihomo")
	if got != "/custom/mihomo" {
		t.Fatalf("expected override to win, got %q", got)
	}
}

func TestResolveMihomoBinaryFindsRepoLocalBinary(t *testing.T) {
	baseDir := t.TempDir()
	binDir := filepath.Join(baseDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	name := mihomoBinaryName(runtime.GOOS)
	path := filepath.Join(binDir, name)
	if err := os.WriteFile(path, []byte("echo test"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatalf("chmod: %v", err)
		}
	}

	got := resolveMihomoBinary(baseDir, t.TempDir(), "")
	if got != path {
		t.Fatalf("expected local binary %q, got %q", path, got)
	}
}

func TestResolveMihomoBinaryPrefersManagedSelection(t *testing.T) {
	baseDir := t.TempDir()
	dataDir := t.TempDir()
	installDir := MihomoInstallDir(dataDir)
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatalf("mkdir install dir: %v", err)
	}

	selected := filepath.Join(installDir, mihomoPlatformBinaryName(runtime.GOOS, runtime.GOARCH))
	if err := os.WriteFile(selected, []byte("echo selected"), 0o755); err != nil {
		t.Fatalf("write selected binary: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(selected, 0o755); err != nil {
			t.Fatalf("chmod selected binary: %v", err)
		}
	}
	if err := os.WriteFile(MihomoBinaryStatePath(dataDir), []byte(selected+"\n"), 0o644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	got := resolveMihomoBinary(baseDir, dataDir, "")
	if got != selected {
		t.Fatalf("expected managed binary %q, got %q", selected, got)
	}
}

func TestMihomoBinaryCandidatesIncludeRepoLocations(t *testing.T) {
	candidates := mihomoBinaryCandidates("/repo", "/data", "windows", "amd64")
	want := []string{
		filepath.Join("/data", "bin", "mihomo.exe"),
		filepath.Join("/repo", "bin", "mihomo.exe"),
		filepath.Join("/repo", "tools", "mihomo.exe"),
		filepath.Join("/repo", "deployments", "bin", "mihomo.exe"),
		filepath.Join("/repo", "mihomo.exe"),
	}

	for _, item := range want {
		if !containsString(candidates, item) {
			t.Fatalf("expected %q in %v", item, candidates)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
