package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	defaultPanelHost                     = "0.0.0.0"
	defaultPanelPort                     = 7891
	defaultLatencyURL                    = "https://cp.cloudflare.com/generate_204"
	defaultLatencyTimeoutMS              = 5000
	defaultLatencyConcurrency            = 32
	defaultSpeedURL                      = "https://speed.cloudflare.com/__down?bytes=5000000"
	defaultSpeedTimeoutMS                = 10000
	defaultSpeedConcurrency              = 1
	MaxProbeSpeedSlots                   = 4
	defaultSubscriptionIntervalSec       = 3600
	defaultProbeControllerAddr           = "127.0.0.1:19091"
	defaultProdControllerAddr            = "127.0.0.1:19090"
	defaultProbeMixedPort                = 17891
	defaultSessionMaxAgeSec = 7 * 24 * 3600 // 7 days persistent login
	defaultLogLevel                      = "info"
	defaultSpeedMaxBytes           int64 = 5000000
	defaultRedisAddr                     = "127.0.0.1:6379"
	defaultScrapeIntervalSec             = 300
	defaultValidateIntervalSec           = 120
	// HTTPS forces CONNECT through upstream HTTP proxies, matching DirectProxy HTTPS usage.
	defaultFreeValidateURL = "https://www.gstatic.com/generate_204"
	defaultFreeValidateTimeoutMS         = 8000
	defaultFreeValidateConcurrency       = 32
	defaultDirectProxyAddr = "0.0.0.0:7892"
	defaultProxyChainAddr  = "0.0.0.0:7893"
	defaultProxyChainHops  = 2
)

var allowedLogLevels = []string{"trace", "debug", "info", "warning", "warn", "error", "silent"}

type App struct {
	PanelHost                 string
	PanelPort                 int
	DataDir                   string
	DBPath                    string
	RuntimeDir                string
	ProdConfigPath            string
	ProbeConfigPath           string
	MihomoInstallDir          string
	MihomoBinaryStatePath     string
	MihomoBinaryPath          string
	ProdControllerAddr        string
	ProbeControllerAddr       string
	ProbeMixedPort            int
	SessionMaxAgeSec          int
	DefaultControllerSecret   string
	RedisAddr                 string
	RedisPassword             string
	RedisDB                   int
	FreeProxyEnabled          bool
	ScrapeIntervalSec         int
	ValidateIntervalSec       int
	FreeValidateURL           string
	FreeValidateTimeoutMS     int
	FreeValidateConcurrency   int
	DirectProxyEnabled        bool
	DirectProxyAddr           string
	DirectProxyUsername       string
	DirectProxyPassword       string
	ProxyChainEnabled         bool
	ProxyChainAddr            string
	ProxyChainHops            int
}

func Load() App {
	dataDir := getenv("DATA_DIR", defaultDataDir())
	runtimeDir := filepath.Join(dataDir, "runtime")
	cwd, _ := os.Getwd()

	return App{
		PanelHost:               getenv("PANEL_HOST", defaultPanelHost),
		PanelPort:               getenvInt("PANEL_PORT", defaultPanelPort),
		DataDir:                 dataDir,
		DBPath:                  getenv("DB_PATH", filepath.Join(dataDir, "app.db")),
		RuntimeDir:              runtimeDir,
		ProdConfigPath:          filepath.Join(runtimeDir, "mihomo-prod.yaml"),
		ProbeConfigPath:         filepath.Join(runtimeDir, "mihomo-probe.yaml"),
		MihomoInstallDir:        MihomoInstallDir(dataDir),
		MihomoBinaryStatePath:   MihomoBinaryStatePath(dataDir),
		MihomoBinaryPath:        resolveMihomoBinary(cwd, dataDir, os.Getenv("MIHOMO_BINARY")),
		ProdControllerAddr:      getenv("PROD_CONTROLLER_ADDR", defaultProdControllerAddr),
		ProbeControllerAddr:     getenv("PROBE_CONTROLLER_ADDR", defaultProbeControllerAddr),
		ProbeMixedPort:          getenvInt("PROBE_MIXED_PORT", defaultProbeMixedPort),
		SessionMaxAgeSec:        getenvInt("SESSION_MAX_AGE_SEC", defaultSessionMaxAgeSec),
		DefaultControllerSecret: getenv("DEFAULT_CONTROLLER_SECRET", randomHex(24)),
		RedisAddr:               getenv("REDIS_ADDR", defaultRedisAddr),
		RedisPassword:           os.Getenv("REDIS_PASSWORD"),
		RedisDB:                 getenvInt("REDIS_DB", 0),
		FreeProxyEnabled:        getenvBool("FREE_PROXY_ENABLED", true),
		ScrapeIntervalSec:       getenvInt("SCRAPE_INTERVAL_SEC", defaultScrapeIntervalSec),
		ValidateIntervalSec:     getenvInt("VALIDATE_INTERVAL_SEC", defaultValidateIntervalSec),
		FreeValidateURL:         getenv("FREE_VALIDATE_URL", defaultFreeValidateURL),
		FreeValidateTimeoutMS:   getenvInt("FREE_VALIDATE_TIMEOUT_MS", defaultFreeValidateTimeoutMS),
		FreeValidateConcurrency: getenvInt("FREE_VALIDATE_CONCURRENCY", defaultFreeValidateConcurrency),
		DirectProxyEnabled:      getenvBool("DIRECT_PROXY_ENABLED", true),
		DirectProxyAddr:         getenv("DIRECT_PROXY_ADDR", defaultDirectProxyAddr),
		DirectProxyUsername:     os.Getenv("DIRECT_PROXY_USERNAME"),
		DirectProxyPassword:     os.Getenv("DIRECT_PROXY_PASSWORD"),
		ProxyChainEnabled:       getenvBool("PROXY_CHAIN_ENABLED", true),
		ProxyChainAddr:          getenv("PROXY_CHAIN_ADDR", defaultProxyChainAddr),
		ProxyChainHops:          getenvInt("PROXY_CHAIN_HOPS", defaultProxyChainHops),
	}
}

func EnsureDirs(cfg App) error {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.RuntimeDir, 0o755); err != nil {
		return err
	}
	return nil
}

func DefaultLatencyURL() string           { return defaultLatencyURL }
func DefaultLatencyTimeoutMS() int        { return defaultLatencyTimeoutMS }
func DefaultLatencyConcurrency() int      { return defaultLatencyConcurrency }
func DefaultSpeedURL() string             { return defaultSpeedURL }
func DefaultSpeedTimeoutMS() int          { return defaultSpeedTimeoutMS }
func DefaultSpeedConcurrency() int        { return defaultSpeedConcurrency }
func DefaultSubscriptionIntervalSec() int { return defaultSubscriptionIntervalSec }
func DefaultSpeedMaxBytes() int64         { return defaultSpeedMaxBytes }

func AllowedLogLevels() []string {
	return append([]string(nil), allowedLogLevels...)
}

func MihomoInstallDir(dataDir string) string {
	return filepath.Join(dataDir, "bin")
}

func MihomoBinaryStatePath(dataDir string) string {
	return filepath.Join(dataDir, "mihomo-binary.txt")
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			return parsed
		}
	}
	return fallback
}

func getenvBool(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func defaultDataDir() string {
	if runtime.GOOS == "windows" {
		if cwd, err := os.Getwd(); err == nil {
			return filepath.Join(cwd, "data")
		}
	}
	return "/data"
}

func defaultMihomoBinary() string {
	return defaultMihomoBinaryFor(runtime.GOOS)
}

func defaultMihomoBinaryFor(goos string) string {
	if goos == "windows" {
		return "mihomo.exe"
	}
	return "/usr/local/bin/mihomo"
}

func resolveMihomoBinary(baseDir, dataDir, override string) string {
	if override != "" {
		return override
	}
	if managed := managedMihomoBinaryPath(dataDir); managed != "" {
		if resolved, err := exec.LookPath(managed); err == nil {
			return resolved
		}
	}
	for _, candidate := range mihomoBinaryCandidates(baseDir, dataDir, runtime.GOOS, runtime.GOARCH) {
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved
		}
	}
	return defaultMihomoBinary()
}

func managedMihomoBinaryPath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	content, err := os.ReadFile(MihomoBinaryStatePath(dataDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}

func mihomoBinaryCandidates(baseDir, dataDir, goos, goarch string) []string {
	name := mihomoBinaryName(goos)
	platformName := mihomoPlatformBinaryName(goos, goarch)
	type candidateRoot struct {
		base      string
		locations [][]string
	}
	roots := []candidateRoot{
		{
			base: dataDir,
			locations: [][]string{
				{"bin"},
			},
		},
		{
			base: baseDir,
			locations: [][]string{
				{"bin"},
				{"tools"},
				{"deployments", "bin"},
				nil,
			},
		},
	}
	var candidates []string
	for _, root := range roots {
		if root.base == "" {
			continue
		}
		for _, location := range root.locations {
			for _, binaryName := range []string{name, platformName} {
				if binaryName == "" {
					continue
				}
				parts := append([]string{root.base}, location...)
				parts = append(parts, binaryName)
				candidates = append(candidates, filepath.Join(parts...))
			}
		}
	}
	candidates = append(candidates, defaultMihomoBinaryFor(goos), name)
	if goos == "windows" {
		candidates = append(candidates, "mihomo")
	}
	if platformName != "" {
		candidates = append(candidates, platformName)
	}
	return uniqueStrings(candidates)
}

func mihomoBinaryName(goos string) string {
	if goos == "windows" {
		return "mihomo.exe"
	}
	return "mihomo"
}

func mihomoPlatformBinaryName(goos, goarch string) string {
	if goos == "" || goarch == "" {
		return ""
	}
	name := "mihomo-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "unified-proxy-pool-secret"
	}
	return hex.EncodeToString(buf)
}

func NormalizeLogLevel(level string) string {
	if normalized, ok := parseLogLevel(level); ok {
		return normalized
	}
	return defaultLogLevel
}

func IsAllowedLogLevel(level string) bool {
	_, ok := parseLogLevel(level)
	return ok
}

func parseLogLevel(level string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(level))
	for _, allowed := range allowedLogLevels {
		if normalized == allowed {
			return normalized, true
		}
	}
	return "", false
}
