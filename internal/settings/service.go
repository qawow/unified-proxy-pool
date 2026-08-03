package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"unified-proxy-pool/internal/config"
	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/features"
	"unified-proxy-pool/internal/models"
)

type Service struct {
	store *db.Store
	cfg   config.App
}

func NewService(store *db.Store, cfg config.App) *Service {
	return &Service{store: store, cfg: cfg}
}

func (s *Service) EnsureDefaults(ctx context.Context, passwordHash string) error {
	var count int
	if err := s.store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE id = 1`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	now := time.Now().UTC()
	_, err := s.store.DB.ExecContext(ctx, `INSERT INTO settings (
		id, panel_host, panel_port, password_hash, speed_test_enabled, latency_test_url, speed_test_url,
		latency_timeout_ms, speed_timeout_ms, latency_concurrency, speed_concurrency,
		default_subscription_interval_sec, mihomo_controller_secret, failure_retry_count, log_level,
		speed_max_bytes, session_max_age_sec, scrape_interval_sec, validate_interval_sec,
		free_validate_url, free_validate_timeout_ms, free_validate_concurrency, proxy_chain_hops,
		created_at, updated_at
	) VALUES (1, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, 2, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.cfg.PanelHost,
		s.cfg.PanelPort,
		passwordHash,
		config.DefaultLatencyURL(),
		config.DefaultSpeedURL(),
		config.DefaultLatencyTimeoutMS(),
		config.DefaultSpeedTimeoutMS(),
		config.DefaultLatencyConcurrency(),
		config.DefaultSpeedConcurrency(),
		config.DefaultSubscriptionIntervalSec(),
		s.cfg.DefaultControllerSecret,
		config.NormalizeLogLevel(""),
		config.DefaultSpeedMaxBytes(),
		s.cfg.SessionMaxAgeSec,
		s.cfg.ScrapeIntervalSec,
		s.cfg.ValidateIntervalSec,
		s.cfg.FreeValidateURL,
		s.cfg.FreeValidateTimeoutMS,
		s.cfg.FreeValidateConcurrency,
		s.cfg.ProxyChainHops,
		now,
		now,
	)
	return err
}

func (s *Service) Get(ctx context.Context) (models.Settings, error) {
	row := s.store.DB.QueryRowContext(ctx, `SELECT id, panel_host, panel_port, password_hash, speed_test_enabled,
		latency_test_url, speed_test_url, latency_timeout_ms, speed_timeout_ms, latency_concurrency,
		speed_concurrency, default_subscription_interval_sec, mihomo_controller_secret, failure_retry_count,
		log_level, speed_max_bytes,
		COALESCE(session_max_age_sec, 0), COALESCE(scrape_interval_sec, 0), COALESCE(validate_interval_sec, 0),
		COALESCE(free_validate_url, ''), COALESCE(free_validate_timeout_ms, 0), COALESCE(free_validate_concurrency, 0),
		COALESCE(proxy_chain_hops, 0), COALESCE(feature_json, '{}'),
		created_at, updated_at FROM settings WHERE id = 1`)
	item, err := scanSettings(row)
	if err != nil {
		return item, err
	}
	s.fillDefaults(&item)
	s.attachFeature(&item)
	return item, nil
}

func (s *Service) FeatureConfig(ctx context.Context) features.Config {
	item, err := s.Get(ctx)
	if err != nil {
		return features.Default()
	}
	return features.Parse(item.FeatureJSON)
}

func (s *Service) attachFeature(item *models.Settings) {
	cfg := features.Parse(item.FeatureJSON)
	item.FeatureJSON = cfg.Marshal()
	var m map[string]any
	_ = json.Unmarshal([]byte(item.FeatureJSON), &m)
	item.Feature = m
}

func (s *Service) fillDefaults(item *models.Settings) {
	if item.SessionMaxAgeSec <= 0 {
		item.SessionMaxAgeSec = s.cfg.SessionMaxAgeSec
		if item.SessionMaxAgeSec <= 0 {
			item.SessionMaxAgeSec = 7 * 24 * 3600
		}
	}
	if item.ScrapeIntervalSec <= 0 {
		item.ScrapeIntervalSec = s.cfg.ScrapeIntervalSec
		if item.ScrapeIntervalSec <= 0 {
			item.ScrapeIntervalSec = 300
		}
	}
	if item.ValidateIntervalSec <= 0 {
		item.ValidateIntervalSec = s.cfg.ValidateIntervalSec
		if item.ValidateIntervalSec <= 0 {
			item.ValidateIntervalSec = 120
		}
	}
	if strings.TrimSpace(item.FreeValidateURL) == "" {
		item.FreeValidateURL = s.cfg.FreeValidateURL
		if item.FreeValidateURL == "" {
			item.FreeValidateURL = "https://www.gstatic.com/generate_204"
		}
	}
	if item.FreeValidateTimeoutMS <= 0 {
		item.FreeValidateTimeoutMS = s.cfg.FreeValidateTimeoutMS
		if item.FreeValidateTimeoutMS <= 0 {
			item.FreeValidateTimeoutMS = 8000
		}
	}
	if item.FreeValidateConcurrency <= 0 {
		item.FreeValidateConcurrency = s.cfg.FreeValidateConcurrency
		if item.FreeValidateConcurrency <= 0 {
			item.FreeValidateConcurrency = 32
		}
	}
	if item.ProxyChainHops < 2 {
		item.ProxyChainHops = s.cfg.ProxyChainHops
		if item.ProxyChainHops < 2 {
			item.ProxyChainHops = 2
		}
	}
	if item.ProxyChainHops > 4 {
		item.ProxyChainHops = 4
	}
}

func (s *Service) Update(ctx context.Context, current models.Settings) (models.Settings, bool, error) {
	existing, err := s.Get(ctx)
	if err != nil {
		return models.Settings{}, false, err
	}
	restartRequired := existing.PanelHost != current.PanelHost || existing.PanelPort != current.PanelPort
	current.ID = 1
	current.PasswordHash = existing.PasswordHash
	current.PanelHost = strings.TrimSpace(current.PanelHost)
	current.LatencyTestURL = strings.TrimSpace(current.LatencyTestURL)
	current.SpeedTestURL = strings.TrimSpace(current.SpeedTestURL)
	current.MihomoControllerSecret = strings.TrimSpace(current.MihomoControllerSecret)
	current.FreeValidateURL = strings.TrimSpace(current.FreeValidateURL)
	current.LogLevel = config.NormalizeLogLevel(current.LogLevel)
	if current.SpeedMaxBytes <= 0 {
		current.SpeedMaxBytes = config.DefaultSpeedMaxBytes()
	}
	// merge feature_json from raw string or Feature map
	if current.FeatureJSON == "" && current.Feature != nil {
		if b, err := json.Marshal(current.Feature); err == nil {
			current.FeatureJSON = string(b)
		}
	}
	if current.FeatureJSON == "" {
		current.FeatureJSON = existing.FeatureJSON
	}
	current.FeatureJSON = features.Parse(current.FeatureJSON).Marshal()
	s.fillDefaults(&current)
	if err := validateSettings(current); err != nil {
		return models.Settings{}, false, err
	}
	current.UpdatedAt = time.Now().UTC()
	_, err = s.store.DB.ExecContext(ctx, `UPDATE settings SET
		panel_host = ?, panel_port = ?, speed_test_enabled = ?, latency_test_url = ?, speed_test_url = ?,
		latency_timeout_ms = ?, speed_timeout_ms = ?, latency_concurrency = ?, speed_concurrency = ?,
		default_subscription_interval_sec = ?, mihomo_controller_secret = ?, failure_retry_count = ?,
		log_level = ?, speed_max_bytes = ?,
		session_max_age_sec = ?, scrape_interval_sec = ?, validate_interval_sec = ?,
		free_validate_url = ?, free_validate_timeout_ms = ?, free_validate_concurrency = ?, proxy_chain_hops = ?,
		feature_json = ?,
		updated_at = ? WHERE id = 1`,
		current.PanelHost, current.PanelPort, boolToInt(current.SpeedTestEnabled), current.LatencyTestURL, current.SpeedTestURL,
		current.LatencyTimeoutMS, current.SpeedTimeoutMS, current.LatencyConcurrency, current.SpeedConcurrency,
		current.DefaultSubscriptionIntervalSec, current.MihomoControllerSecret, current.FailureRetryCount,
		current.LogLevel, current.SpeedMaxBytes,
		current.SessionMaxAgeSec, current.ScrapeIntervalSec, current.ValidateIntervalSec,
		current.FreeValidateURL, current.FreeValidateTimeoutMS, current.FreeValidateConcurrency, current.ProxyChainHops,
		current.FeatureJSON,
		current.UpdatedAt,
	)
	if err != nil {
		return models.Settings{}, false, err
	}
	updated, err := s.Get(ctx)
	return updated, restartRequired, err
}

func (s *Service) UpdatePasswordHash(ctx context.Context, hash string) error {
	_, err := s.store.DB.ExecContext(ctx, `UPDATE settings SET password_hash = ?, updated_at = ? WHERE id = 1`, hash, time.Now().UTC())
	return err
}

func scanSettings(scanner interface{ Scan(dest ...any) error }) (models.Settings, error) {
	var item models.Settings
	var speedEnabled int
	err := scanner.Scan(
		&item.ID, &item.PanelHost, &item.PanelPort, &item.PasswordHash, &speedEnabled,
		&item.LatencyTestURL, &item.SpeedTestURL, &item.LatencyTimeoutMS, &item.SpeedTimeoutMS,
		&item.LatencyConcurrency, &item.SpeedConcurrency, &item.DefaultSubscriptionIntervalSec,
		&item.MihomoControllerSecret, &item.FailureRetryCount, &item.LogLevel, &item.SpeedMaxBytes,
		&item.SessionMaxAgeSec, &item.ScrapeIntervalSec, &item.ValidateIntervalSec,
		&item.FreeValidateURL, &item.FreeValidateTimeoutMS, &item.FreeValidateConcurrency,
		&item.ProxyChainHops, &item.FeatureJSON,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return models.Settings{}, err
	}
	item.SpeedTestEnabled = speedEnabled == 1
	return item, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func validateSettings(item models.Settings) error {
	if item.PanelHost == "" {
		return errors.New("panel_host is required")
	}
	if item.PanelPort < 1 || item.PanelPort > 65535 {
		return fmt.Errorf("panel_port must be between 1 and 65535")
	}
	if item.LatencyTestURL == "" {
		return errors.New("latency_test_url is required")
	}
	if item.SpeedTestURL == "" {
		return errors.New("speed_test_url is required")
	}
	if item.LatencyTimeoutMS <= 0 {
		return errors.New("latency_timeout_ms must be greater than zero")
	}
	if item.SpeedTimeoutMS <= 0 {
		return errors.New("speed_timeout_ms must be greater than zero")
	}
	if item.LatencyConcurrency <= 0 {
		return errors.New("latency_concurrency must be greater than zero")
	}
	if item.SpeedConcurrency <= 0 {
		return errors.New("speed_concurrency must be greater than zero")
	}
	if item.SpeedConcurrency > config.MaxProbeSpeedSlots {
		return fmt.Errorf("speed_concurrency must be less than or equal to %d", config.MaxProbeSpeedSlots)
	}
	if item.DefaultSubscriptionIntervalSec <= 0 {
		return errors.New("default_subscription_interval_sec must be greater than zero")
	}
	if item.MihomoControllerSecret == "" {
		return errors.New("mihomo_controller_secret is required")
	}
	if item.FailureRetryCount < 0 {
		return errors.New("failure_retry_count must be zero or greater")
	}
	if !config.IsAllowedLogLevel(item.LogLevel) {
		return fmt.Errorf("log_level must be one of %s", strings.Join(config.AllowedLogLevels(), ", "))
	}
	if item.SpeedMaxBytes <= 0 {
		return errors.New("speed_max_bytes must be greater than zero")
	}
	if item.SessionMaxAgeSec < 3600 {
		return errors.New("session_max_age_sec must be at least 3600 (1 hour)")
	}
	if item.ScrapeIntervalSec < 60 {
		return errors.New("scrape_interval_sec must be at least 60")
	}
	if item.ValidateIntervalSec < 30 {
		return errors.New("validate_interval_sec must be at least 30")
	}
	if item.FreeValidateURL == "" {
		return errors.New("free_validate_url is required")
	}
	if item.FreeValidateTimeoutMS < 1000 {
		return errors.New("free_validate_timeout_ms must be at least 1000")
	}
	if item.FreeValidateConcurrency < 1 || item.FreeValidateConcurrency > 256 {
		return errors.New("free_validate_concurrency must be between 1 and 256")
	}
	if item.ProxyChainHops < 2 || item.ProxyChainHops > 4 {
		return errors.New("proxy_chain_hops must be between 2 and 4")
	}
	return nil
}
