package mihomo

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultReleaseAPIURL = "https://api.github.com/repos/MetaCubeX/mihomo/releases/latest"

var goTagPattern = regexp.MustCompile(`-go\d+-`)

type RuntimeStatus struct {
	HostOS          string     `json:"host_os"`
	HostArch        string     `json:"host_arch"`
	BinaryPath      string     `json:"binary_path"`
	BinaryAvailable bool       `json:"binary_available"`
	RuntimeDir      string     `json:"runtime_dir"`
	ProdRunning     bool       `json:"prod_running"`
	ProbeRunning    bool       `json:"probe_running"`
	ProdPID         int        `json:"prod_pid,omitempty"`
	ProbePID        int        `json:"probe_pid,omitempty"`
	LastProdExit    string     `json:"last_prod_exit,omitempty"`
	LastProbeExit   string     `json:"last_probe_exit,omitempty"`
	LastProdExitAt  *time.Time `json:"last_prod_exit_at,omitempty"`
	LastProbeExitAt *time.Time `json:"last_probe_exit_at,omitempty"`
}

type ReleaseInfo struct {
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	PublishedAt time.Time      `json:"published_at"`
	HostOS      string         `json:"host_os"`
	HostArch    string         `json:"host_arch"`
	InstallDir  string         `json:"install_dir"`
	Assets      []ReleaseAsset `json:"assets"`
}

type ReleaseAsset struct {
	Name          string   `json:"name"`
	DownloadURL   string   `json:"download_url"`
	Size          int64    `json:"size"`
	InstallPath   string   `json:"install_path"`
	ArchiveFormat string   `json:"archive_format"`
	Notes         []string `json:"notes,omitempty"`
	Recommended   bool     `json:"recommended"`
}

type InstallResult struct {
	ReleaseTag       string       `json:"release_tag"`
	InstalledPath    string       `json:"installed_path"`
	AlreadyInstalled bool         `json:"already_installed"`
	Asset            ReleaseAsset `json:"asset"`
}

type Installer struct {
	client        *http.Client
	releaseAPIURL string
	installDir    string
	selectionPath string
	hostOS        string
	hostArch      string
	mu            sync.Mutex
}

type InstallerOptions struct {
	HTTPClient    *http.Client
	ReleaseAPIURL string
	InstallDir    string
	SelectionPath string
	HostOS        string
	HostArch      string
}

func NewInstaller(installDir, selectionPath string) *Installer {
	return NewInstallerWithOptions(InstallerOptions{
		InstallDir:    installDir,
		SelectionPath: selectionPath,
	})
}

func NewInstallerWithOptions(opts InstallerOptions) *Installer {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	releaseAPIURL := opts.ReleaseAPIURL
	if releaseAPIURL == "" {
		releaseAPIURL = defaultReleaseAPIURL
	}
	hostOS := opts.HostOS
	if hostOS == "" {
		hostOS = runtime.GOOS
	}
	hostArch := opts.HostArch
	if hostArch == "" {
		hostArch = runtime.GOARCH
	}
	return &Installer{
		client:        client,
		releaseAPIURL: releaseAPIURL,
		installDir:    opts.InstallDir,
		selectionPath: opts.SelectionPath,
		hostOS:        hostOS,
		hostArch:      hostArch,
	}
}

func (i *Installer) LatestRelease(ctx context.Context) (ReleaseInfo, error) {
	release, err := i.fetchLatestRelease(ctx)
	if err != nil {
		return ReleaseInfo{}, err
	}

	info := ReleaseInfo{
		TagName:     release.TagName,
		Name:        release.Name,
		PublishedAt: release.PublishedAt,
		HostOS:      i.hostOS,
		HostArch:    i.hostArch,
		InstallDir:  i.installDir,
		Assets:      make([]ReleaseAsset, 0, len(release.Assets)),
	}

	for _, asset := range release.Assets {
		format := archiveFormat(asset.Name)
		if format == "" || !assetMatchesHost(asset.Name, i.hostOS, i.hostArch) {
			continue
		}
		info.Assets = append(info.Assets, ReleaseAsset{
			Name:          asset.Name,
			DownloadURL:   asset.BrowserDownloadURL,
			Size:          asset.Size,
			InstallPath:   filepath.Join(i.installDir, destinationBinaryName(asset.Name, i.hostOS)),
			ArchiveFormat: format,
			Notes:         describeAssetVariant(asset.Name),
		})
	}

	sort.SliceStable(info.Assets, func(left, right int) bool {
		leftScore := assetScore(info.Assets[left].Name, i.hostOS, i.hostArch)
		rightScore := assetScore(info.Assets[right].Name, i.hostOS, i.hostArch)
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		return info.Assets[left].Name < info.Assets[right].Name
	})

	if len(info.Assets) > 0 {
		info.Assets[0].Recommended = true
	}

	return info, nil
}

func (i *Installer) Install(ctx context.Context, assetName string) (InstallResult, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	release, err := i.LatestRelease(ctx)
	if err != nil {
		return InstallResult{}, err
	}
	if len(release.Assets) == 0 {
		return InstallResult{}, errors.New("no compatible mihomo assets found for this host")
	}

	asset, err := pickAsset(release.Assets, assetName)
	if err != nil {
		return InstallResult{}, err
	}

	if err := os.MkdirAll(i.installDir, 0o755); err != nil {
		return InstallResult{}, err
	}

	if info, statErr := os.Stat(asset.InstallPath); statErr == nil && info.Mode().IsRegular() {
		if err := writeFileAtomic(i.selectionPath, []byte(asset.InstallPath)); err != nil {
			return InstallResult{}, err
		}
		return InstallResult{
			ReleaseTag:       release.TagName,
			InstalledPath:    asset.InstallPath,
			AlreadyInstalled: true,
			Asset:            asset,
		}, nil
	}

	archivePath := filepath.Join(i.installDir, "."+filepath.Base(asset.Name)+".download")
	tmpBinaryPath := asset.InstallPath + ".tmp"
	defer os.Remove(archivePath)
	defer os.Remove(tmpBinaryPath)

	if err := i.downloadAsset(ctx, asset.DownloadURL, archivePath); err != nil {
		return InstallResult{}, err
	}
	if err := extractArchive(archivePath, tmpBinaryPath, asset.ArchiveFormat); err != nil {
		return InstallResult{}, err
	}
	if i.hostOS != "windows" {
		if err := os.Chmod(tmpBinaryPath, 0o755); err != nil {
			return InstallResult{}, err
		}
	}
	if err := replaceFile(tmpBinaryPath, asset.InstallPath); err != nil {
		return InstallResult{}, err
	}
	if err := writeFileAtomic(i.selectionPath, []byte(asset.InstallPath)); err != nil {
		return InstallResult{}, err
	}

	return InstallResult{
		ReleaseTag:    release.TagName,
		InstalledPath: asset.InstallPath,
		Asset:         asset,
	}, nil
}

func (i *Installer) downloadAsset(ctx context.Context, downloadURL, destination string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "unified-proxy-pool")

	resp, err := i.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("download mihomo asset failed: %s", strings.TrimSpace(string(body)))
	}

	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	return err
}

func (i *Installer) fetchLatestRelease(ctx context.Context) (githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.releaseAPIURL, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "unified-proxy-pool")

	resp, err := i.client.Do(req)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return githubRelease{}, fmt.Errorf("fetch latest mihomo release failed: %s", strings.TrimSpace(string(body)))
	}

	var payload githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return githubRelease{}, err
	}
	return payload, nil
}

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	PublishedAt time.Time     `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

func pickAsset(assets []ReleaseAsset, assetName string) (ReleaseAsset, error) {
	if assetName != "" {
		for _, asset := range assets {
			if asset.Name == assetName {
				return asset, nil
			}
		}
		return ReleaseAsset{}, fmt.Errorf("mihomo asset %q not found for this host", assetName)
	}
	for _, asset := range assets {
		if asset.Recommended {
			return asset, nil
		}
	}
	return assets[0], nil
}

func extractArchive(archivePath, destinationPath, format string) error {
	switch format {
	case "gz":
		return extractGzip(archivePath, destinationPath)
	case "zip":
		return extractZip(archivePath, destinationPath)
	default:
		return fmt.Errorf("unsupported archive format for %s", archivePath)
	}
}

func extractGzip(archivePath, destinationPath string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	reader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer reader.Close()

	return copyToFile(destinationPath, reader)
}

func extractZip(archivePath, destinationPath string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		return err
	}

	for _, candidate := range reader.File {
		if candidate.FileInfo().IsDir() {
			continue
		}
		base := strings.ToLower(filepath.Base(candidate.Name))
		if base == "mihomo" || base == "mihomo.exe" || strings.HasPrefix(base, "mihomo-") {
			rc, err := candidate.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			return copyToFile(destinationPath, rc)
		}
	}

	return errors.New("mihomo binary not found in archive")
}

func copyToFile(destinationPath string, src io.Reader) error {
	file, err := os.Create(destinationPath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(file, src)
	return err
}

func replaceFile(sourcePath, destinationPath string) error {
	if err := os.Rename(sourcePath, destinationPath); err == nil {
		return nil
	}
	_ = os.Remove(destinationPath)
	return os.Rename(sourcePath, destinationPath)
}

func archiveFormat(name string) string {
	lower := strings.ToLower(filepath.Base(name))
	switch {
	case strings.HasSuffix(lower, ".gz"):
		return "gz"
	case strings.HasSuffix(lower, ".zip"):
		return "zip"
	default:
		return ""
	}
}

func destinationBinaryName(assetName, hostOS string) string {
	base := filepath.Base(assetName)
	switch {
	case strings.HasSuffix(strings.ToLower(base), ".gz"):
		base = base[:len(base)-3]
	case strings.HasSuffix(strings.ToLower(base), ".zip"):
		base = base[:len(base)-4]
	}
	if hostOS == "windows" && !strings.HasSuffix(strings.ToLower(base), ".exe") {
		base += ".exe"
	}
	return base
}

func assetMatchesHost(name, hostOS, hostArch string) bool {
	lower := strings.ToLower(name)
	prefix := "mihomo-" + hostOS + "-"
	if !strings.HasPrefix(lower, prefix) {
		return false
	}
	for _, alias := range archAliases(hostArch) {
		if strings.Contains(lower, "-"+alias+"-") {
			return true
		}
	}
	if hostArch == "arm" {
		return strings.Contains(lower, "-arm") && !strings.Contains(lower, "-arm64")
	}
	return false
}

func archAliases(goarch string) []string {
	switch goarch {
	case "amd64":
		return []string{"amd64"}
	case "386":
		return []string{"386"}
	case "arm64":
		return []string{"arm64", "arm64-v8"}
	case "arm":
		return []string{"armv7", "arm32v7", "arm"}
	default:
		return []string{goarch}
	}
}

func assetScore(name, hostOS, hostArch string) int {
	score := 0
	if strings.Contains(name, "-"+hostArch+"-") {
		score += 100
	} else {
		score += 80
	}
	if !strings.Contains(name, "-compatible-") {
		score += 10
	}
	if !goTagPattern.MatchString(name) {
		score += 8
	}
	if hostArch == "amd64" && !containsAMD64Level(name) {
		score += 6
	}
	if hostOS == "windows" && strings.HasSuffix(strings.ToLower(name), ".zip") {
		score += 2
	}
	if hostOS != "windows" && strings.HasSuffix(strings.ToLower(name), ".gz") {
		score += 2
	}
	return score
}

func containsAMD64Level(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "-v1-") || strings.Contains(lower, "-v2-") || strings.Contains(lower, "-v3-")
}

func describeAssetVariant(name string) []string {
	notes := make([]string, 0, 3)
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "-compatible-"):
		notes = append(notes, "compatible")
	case strings.Contains(lower, "-v1-"):
		notes = append(notes, "amd64 v1")
	case strings.Contains(lower, "-v2-"):
		notes = append(notes, "amd64 v2")
	case strings.Contains(lower, "-v3-"):
		notes = append(notes, "amd64 v3")
	default:
		notes = append(notes, "default")
	}
	if match := goTagPattern.FindString(lower); match != "" {
		notes = append(notes, strings.Trim(match, "-"))
	}
	format := archiveFormat(name)
	if format != "" {
		notes = append(notes, format)
	}
	return notes
}
