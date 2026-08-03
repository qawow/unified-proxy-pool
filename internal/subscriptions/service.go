package subscriptions

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/events"
	"unified-proxy-pool/internal/models"
	"unified-proxy-pool/internal/nodes"
	"unified-proxy-pool/internal/settings"
)

type Service struct {
	store          *db.Store
	settingsSvc    *settings.Service
	events         *events.Broker
	client         *http.Client
	mu             sync.Mutex
	syncing        map[int64]struct{}
	afterSyncHooks []func(context.Context, int64, []int64)
}

type UpsertRequest struct {
	Name            string `json:"name"`
	URL             string `json:"url"`
	HeadersJSON     string `json:"headers_json"`
	Enabled         bool   `json:"enabled"`
	SyncIntervalSec int    `json:"sync_interval_sec"`
}

type SyncOutcome struct {
	Status       string   `json:"status"`
	Modified     bool     `json:"modified"`
	CreatedCount int      `json:"created_count"`
	FailedCount  int      `json:"failed_count"`
	Errors       []string `json:"errors"`
}

type storedSubscriptionNode struct {
	ID             int64
	DisplayName    string
	Protocol       string
	Server         string
	Port           int
	RawPayload     string
	NormalizedJSON string
}

func NewService(store *db.Store, settingsSvc *settings.Service, broker *events.Broker) *Service {
	return &Service{
		store:       store,
		settingsSvc: settingsSvc,
		events:      broker,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				Proxy:             http.ProxyFromEnvironment,
				ForceAttemptHTTP2: false,
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
				},
			},
		},
		syncing: make(map[int64]struct{}),
	}
}

func (s *Service) SetAfterSyncHook(fn func(context.Context, int64, []int64)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fn == nil {
		s.afterSyncHooks = nil
		return
	}
	s.afterSyncHooks = []func(context.Context, int64, []int64){fn}
}

func (s *Service) AddAfterSyncHook(fn func(context.Context, int64, []int64)) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.afterSyncHooks = append(s.afterSyncHooks, fn)
}

func (s *Service) StartScheduler(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		s.runDueSyncs(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runDueSyncs(ctx)
			}
		}
	}()
}

func (s *Service) List(ctx context.Context) ([]models.Subscription, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id, name, url, headers_json, enabled, sync_interval_sec, last_sync_at,
		last_sync_status, last_error, etag, last_modified, created_at, updated_at
		FROM subscriptions ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []models.Subscription
	for rows.Next() {
		item, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) ListWithStats(ctx context.Context) ([]models.SubscriptionListItem, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT s.id, s.name, s.url, s.headers_json, s.enabled, s.sync_interval_sec,
		s.last_sync_at, s.last_sync_status, s.last_error, s.etag, s.last_modified, s.created_at, s.updated_at,
		COUNT(n.id) AS total_nodes,
		SUM(CASE WHEN n.last_status = 'available' THEN 1 ELSE 0 END) AS available_nodes,
		SUM(CASE WHEN n.id IS NOT NULL AND (n.enabled = 0 OR n.last_status = 'unavailable') THEN 1 ELSE 0 END) AS invalid_nodes,
		AVG(n.last_latency_ms) AS average_latency_ms
		FROM subscriptions s
		LEFT JOIN subscription_nodes n ON n.subscription_id = s.id
		GROUP BY s.id, s.name, s.url, s.headers_json, s.enabled, s.sync_interval_sec, s.last_sync_at,
			s.last_sync_status, s.last_error, s.etag, s.last_modified, s.created_at, s.updated_at
		ORDER BY s.updated_at DESC, s.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []models.SubscriptionListItem
	for rows.Next() {
		item, err := scanSubscriptionListItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) Get(ctx context.Context, id int64) (models.Subscription, error) {
	row := s.store.DB.QueryRowContext(ctx, `SELECT id, name, url, headers_json, enabled, sync_interval_sec, last_sync_at,
		last_sync_status, last_error, etag, last_modified, created_at, updated_at
		FROM subscriptions WHERE id = ?`, id)
	return scanSubscription(row)
}

func (s *Service) Create(ctx context.Context, req UpsertRequest) (models.Subscription, error) {
	req, err := s.normalizeUpsertRequest(ctx, req)
	if err != nil {
		return models.Subscription{}, err
	}
	now := time.Now().UTC()
	res, err := s.store.DB.ExecContext(ctx, `INSERT INTO subscriptions (
		name, url, headers_json, enabled, sync_interval_sec, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		req.Name, req.URL, req.HeadersJSON, boolToInt(req.Enabled), req.SyncIntervalSec, now, now,
	)
	if err != nil {
		return models.Subscription{}, err
	}
	id, _ := res.LastInsertId()
	item, err := s.Get(ctx, id)
	if err == nil {
		s.events.Publish("subscriptions.created", item)
	}
	return item, err
}

func (s *Service) Update(ctx context.Context, id int64, req UpsertRequest) (models.Subscription, error) {
	req, err := s.normalizeUpsertRequest(ctx, req)
	if err != nil {
		return models.Subscription{}, err
	}
	_, err = s.store.DB.ExecContext(ctx, `UPDATE subscriptions SET name = ?, url = ?, headers_json = ?, enabled = ?, sync_interval_sec = ?, updated_at = ?
		WHERE id = ?`, req.Name, req.URL, req.HeadersJSON, boolToInt(req.Enabled), req.SyncIntervalSec, time.Now().UTC(), id)
	if err != nil {
		return models.Subscription{}, err
	}
	item, err := s.Get(ctx, id)
	if err == nil {
		s.events.Publish("subscriptions.updated", item)
	}
	return item, err
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	_, err := s.store.DB.ExecContext(ctx, `DELETE FROM subscriptions WHERE id = ?`, id)
	if err == nil {
		s.events.Publish("subscriptions.deleted", map[string]int64{"id": id})
	}
	return err
}

func (s *Service) Toggle(ctx context.Context, id int64) (models.Subscription, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return models.Subscription{}, err
	}
	_, err = s.store.DB.ExecContext(ctx, `UPDATE subscriptions SET enabled = ?, updated_at = ? WHERE id = ?`,
		boolToInt(!current.Enabled), time.Now().UTC(), id)
	if err != nil {
		return models.Subscription{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) ListNodes(ctx context.Context, subscriptionID int64) ([]models.SubscriptionNode, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id, subscription_id, display_name, protocol, server, port, raw_payload, normalized_json,
		enabled, last_latency_ms, last_speed_mbps, last_status, last_test_at, last_speed_at, last_error, created_at, updated_at
		FROM subscription_nodes WHERE subscription_id = ? ORDER BY updated_at DESC, id DESC`, subscriptionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []models.SubscriptionNode
	for rows.Next() {
		item, err := scanSubscriptionNode(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) GetNode(ctx context.Context, subscriptionID, nodeID int64) (models.SubscriptionNode, error) {
	row := s.store.DB.QueryRowContext(ctx, `SELECT id, subscription_id, display_name, protocol, server, port, raw_payload, normalized_json,
		enabled, last_latency_ms, last_speed_mbps, last_status, last_test_at, last_speed_at, last_error, created_at, updated_at
		FROM subscription_nodes WHERE subscription_id = ? AND id = ?`, subscriptionID, nodeID)
	return scanSubscriptionNode(row)
}

func (s *Service) ToggleNode(ctx context.Context, subscriptionID, nodeID int64) (models.SubscriptionNode, error) {
	current, err := s.GetNode(ctx, subscriptionID, nodeID)
	if err != nil {
		return models.SubscriptionNode{}, err
	}
	_, err = s.store.DB.ExecContext(ctx, `UPDATE subscription_nodes SET enabled = ?, updated_at = ? WHERE id = ? AND subscription_id = ?`,
		boolToInt(!current.Enabled), time.Now().UTC(), nodeID, subscriptionID)
	if err != nil {
		return models.SubscriptionNode{}, err
	}
	return s.GetNode(ctx, subscriptionID, nodeID)
}

func (s *Service) Sync(ctx context.Context, id int64) (SyncOutcome, error) {
	if !s.beginSync(id) {
		return SyncOutcome{}, errors.New("subscription sync already running")
	}
	defer s.endSync(id)

	sub, err := s.Get(ctx, id)
	if err != nil {
		return SyncOutcome{}, err
	}
	settingsRow, err := s.settingsSvc.Get(ctx)
	if err != nil {
		return SyncOutcome{}, err
	}
	s.events.Publish("subscriptions.sync.started", map[string]any{"subscription_id": id})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sub.URL, nil)
	if err != nil {
		return SyncOutcome{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Super-Proxy-Pool)")
	for key, value := range parseHeaders(sub.HeadersJSON) {
		req.Header.Set(key, value)
	}
	if sub.ETag != "" {
		req.Header.Set("If-None-Match", sub.ETag)
	}
	if sub.LastModified != "" {
		req.Header.Set("If-Modified-Since", sub.LastModified)
	}

	resp, err := s.doWithRetry(req, settingsRow.FailureRetryCount)
	if err != nil {
		_ = s.setSyncFailure(ctx, sub.ID, err.Error())
		s.publishSyncFailure(sub.ID, err.Error())
		return SyncOutcome{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		now := time.Now().UTC()
		if err := s.setSyncNotModified(ctx, sub.ID, now); err != nil {
			return SyncOutcome{}, err
		}
		outcome := SyncOutcome{
			Status:       "not_modified",
			Modified:     false,
			CreatedCount: 0,
			FailedCount:  0,
			Errors:       nil,
		}
		s.events.Publish("subscriptions.synced", map[string]any{"subscription_id": id, "outcome": outcome})
		return outcome, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("subscription fetch failed: %s", resp.Status)
		_ = s.setSyncFailure(ctx, sub.ID, err.Error())
		s.publishSyncFailure(sub.ID, err.Error())
		return SyncOutcome{}, err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return SyncOutcome{}, err
	}
	result := ParseSubscriptionContent(string(body))
	if len(result.Nodes) == 0 {
		err := errors.New("no nodes parsed from subscription")
		_ = s.setSyncFailure(ctx, sub.ID, err.Error())
		s.publishSyncFailure(sub.ID, err.Error())
		return SyncOutcome{}, err
	}

	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return SyncOutcome{}, err
	}
	defer tx.Rollback()

	existingNodes, err := loadStoredSubscriptionNodes(ctx, tx, sub.ID)
	if err != nil {
		return SyncOutcome{}, err
	}

	existingByFingerprint := make(map[string][]storedSubscriptionNode, len(existingNodes))
	for _, item := range existingNodes {
		fingerprint := subscriptionNodeFingerprint(item.Protocol, item.Server, item.Port, item.NormalizedJSON)
		existingByFingerprint[fingerprint] = append(existingByFingerprint[fingerprint], item)
	}

	now := time.Now().UTC()
	var syncedNodeIDs []int64
	matchedIDs := make(map[int64]struct{}, len(existingNodes))
	for _, item := range result.Nodes {
		normalizedJSON := nodes.NormalizeJSON(item.Normalized)
		fingerprint := subscriptionNodeFingerprint(item.Protocol, item.Server, item.Port, normalizedJSON)
		if existing, ok := popStoredSubscriptionNode(existingByFingerprint[fingerprint], item.RawPayload); ok {
			existingByFingerprint[fingerprint] = removeStoredSubscriptionNode(existingByFingerprint[fingerprint], existing.ID)
			matchedIDs[existing.ID] = struct{}{}
			syncedNodeIDs = append(syncedNodeIDs, existing.ID)
			if needsStoredSubscriptionNodeUpdate(existing, item, normalizedJSON) {
				if _, err := tx.ExecContext(ctx, `UPDATE subscription_nodes
					SET display_name = ?, protocol = ?, server = ?, port = ?, raw_payload = ?, normalized_json = ?, updated_at = ?
					WHERE id = ? AND subscription_id = ?`,
					item.DisplayName, item.Protocol, item.Server, item.Port, item.RawPayload, normalizedJSON, now, existing.ID, sub.ID,
				); err != nil {
					return SyncOutcome{}, err
				}
			}
			continue
		}

		res, err := tx.ExecContext(ctx, `INSERT INTO subscription_nodes (
			subscription_id, display_name, protocol, server, port, raw_payload, normalized_json, enabled, last_status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 1, 'unknown', ?, ?)`,
			sub.ID, item.DisplayName, item.Protocol, item.Server, item.Port, item.RawPayload, normalizedJSON, now, now,
		)
		if err != nil {
			return SyncOutcome{}, err
		}
		nodeID, err := res.LastInsertId()
		if err != nil {
			return SyncOutcome{}, err
		}
		syncedNodeIDs = append(syncedNodeIDs, nodeID)
	}
	for _, item := range existingNodes {
		if _, ok := matchedIDs[item.ID]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM subscription_nodes WHERE id = ? AND subscription_id = ?`, item.ID, sub.ID); err != nil {
			return SyncOutcome{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE subscriptions SET last_sync_at = ?, last_sync_status = ?, last_error = ?, etag = ?, last_modified = ?, updated_at = ?
		WHERE id = ?`, now, "ok", errorSummary(result.Errors), resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"), now, sub.ID); err != nil {
		return SyncOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return SyncOutcome{}, err
	}
	outcome := SyncOutcome{
		Status:       "ok",
		Modified:     true,
		CreatedCount: len(result.Nodes),
		FailedCount:  len(result.Errors),
		Errors:       stringifyErrors(result.Errors),
	}
	s.events.Publish("subscriptions.synced", map[string]any{"subscription_id": id, "outcome": outcome})
	s.mu.Lock()
	hooks := append([]func(context.Context, int64, []int64){}, s.afterSyncHooks...)
	s.mu.Unlock()
	if len(syncedNodeIDs) > 0 {
		nodeIDs := append([]int64(nil), syncedNodeIDs...)
		for _, hook := range hooks {
			if hook == nil {
				continue
			}
			go hook(context.Background(), sub.ID, nodeIDs)
		}
	}
	return outcome, nil
}

func (s *Service) AllRuntimeNodes(ctx context.Context) ([]models.RuntimeNode, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id, display_name, protocol, server, port, raw_payload, normalized_json, enabled, last_status FROM subscription_nodes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.RuntimeNode
	for rows.Next() {
		var item models.RuntimeNode
		item.SourceType = "subscription"
		if err := rows.Scan(&item.SourceNodeID, &item.DisplayName, &item.Protocol, &item.Server, &item.Port, &item.RawPayload, &item.NormalizedJSON, &item.Enabled, &item.LastStatus); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) ListPoolCandidates(ctx context.Context) ([]models.PoolMemberView, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT n.id, n.display_name, n.protocol, n.server, n.port, n.enabled, n.last_status, n.last_latency_ms, n.last_speed_mbps, s.name
		FROM subscription_nodes n JOIN subscriptions s ON s.id = n.subscription_id
		ORDER BY s.name ASC, n.display_name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.PoolMemberView
	for rows.Next() {
		var item models.PoolMemberView
		item.SourceType = "subscription"
		if err := rows.Scan(&item.SourceNodeID, &item.DisplayName, &item.Protocol, &item.Server, &item.Port, &item.Enabled, &item.LastStatus, &item.LastLatencyMS, &item.LastSpeedMbps, &item.SourceLabel); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) NodeBySource(ctx context.Context, id int64) (models.RuntimeNode, error) {
	row := s.store.DB.QueryRowContext(ctx, `SELECT id, display_name, protocol, server, port, raw_payload, normalized_json, enabled, last_status
		FROM subscription_nodes WHERE id = ?`, id)
	var item models.RuntimeNode
	item.SourceType = "subscription"
	err := row.Scan(&item.SourceNodeID, &item.DisplayName, &item.Protocol, &item.Server, &item.Port, &item.RawPayload, &item.NormalizedJSON, &item.Enabled, &item.LastStatus)
	return item, err
}

func (s *Service) UpdateProbeResult(ctx context.Context, sourceNodeID int64, latency *int64, speed *float64, status, errMsg string, isSpeed bool) error {
	now := time.Now().UTC()
	if isSpeed {
		_, err := s.store.DB.ExecContext(ctx, `UPDATE subscription_nodes SET last_speed_mbps = ?, last_speed_at = ?, last_status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
			speed, now, status, errMsg, now, sourceNodeID)
		return err
	}
	_, err := s.store.DB.ExecContext(ctx, `UPDATE subscription_nodes SET last_latency_ms = ?, last_test_at = ?, last_status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		latency, now, status, errMsg, now, sourceNodeID)
	return err
}

func (s *Service) SetTransientStatus(ctx context.Context, sourceNodeID int64, status, errMsg string) error {
	_, err := s.store.DB.ExecContext(ctx, `UPDATE subscription_nodes SET last_status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		status, errMsg, time.Now().UTC(), sourceNodeID)
	return err
}

func (s *Service) setSyncFailure(ctx context.Context, id int64, message string) error {
	_, err := s.store.DB.ExecContext(ctx, `UPDATE subscriptions SET last_sync_status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		"failed", message, time.Now().UTC(), id)
	return err
}

func (s *Service) setSyncNotModified(ctx context.Context, id int64, syncedAt time.Time) error {
	_, err := s.store.DB.ExecContext(ctx, `UPDATE subscriptions
		SET last_sync_at = ?, last_sync_status = ?, last_error = ?, updated_at = ?
		WHERE id = ?`,
		syncedAt, "not_modified", "", syncedAt, id,
	)
	return err
}

func (s *Service) publishSyncFailure(id int64, message string) {
	s.events.Publish("subscriptions.sync.failed", map[string]any{
		"subscription_id": id,
		"message":         message,
	})
}

func scanSubscription(scanner interface{ Scan(dest ...any) error }) (models.Subscription, error) {
	var item models.Subscription
	var enabled int
	var lastSyncAt sql.NullTime
	err := scanner.Scan(&item.ID, &item.Name, &item.URL, &item.HeadersJSON, &enabled, &item.SyncIntervalSec, &lastSyncAt,
		&item.LastSyncStatus, &item.LastError, &item.ETag, &item.LastModified, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return models.Subscription{}, err
	}
	item.Enabled = enabled == 1
	if lastSyncAt.Valid {
		v := lastSyncAt.Time
		item.LastSyncAt = &v
	}
	return item, nil
}

func scanSubscriptionListItem(scanner interface{ Scan(dest ...any) error }) (models.SubscriptionListItem, error) {
	var item models.SubscriptionListItem
	var enabled int
	var lastSyncAt sql.NullTime
	var totalNodes int64
	var availableNodes int64
	var invalidNodes int64
	var averageLatency sql.NullFloat64

	err := scanner.Scan(
		&item.ID, &item.Name, &item.URL, &item.HeadersJSON, &enabled, &item.SyncIntervalSec, &lastSyncAt,
		&item.LastSyncStatus, &item.LastError, &item.ETag, &item.LastModified, &item.CreatedAt, &item.UpdatedAt,
		&totalNodes, &availableNodes, &invalidNodes, &averageLatency,
	)
	if err != nil {
		return models.SubscriptionListItem{}, err
	}

	item.Enabled = enabled == 1
	item.TotalNodes = int(totalNodes)
	item.AvailableNodes = int(availableNodes)
	item.InvalidNodes = int(invalidNodes)
	if lastSyncAt.Valid {
		v := lastSyncAt.Time
		item.LastSyncAt = &v
	}
	if averageLatency.Valid {
		avg := int64(averageLatency.Float64)
		item.AverageLatencyMS = &avg
	}
	return item, nil
}

func scanSubscriptionNode(scanner interface{ Scan(dest ...any) error }) (models.SubscriptionNode, error) {
	var item models.SubscriptionNode
	var enabled int
	var latency sql.NullInt64
	var speed sql.NullFloat64
	var lastTestAt sql.NullTime
	var lastSpeedAt sql.NullTime
	err := scanner.Scan(
		&item.ID, &item.SubscriptionID, &item.DisplayName, &item.Protocol, &item.Server, &item.Port, &item.RawPayload, &item.NormalizedJSON,
		&enabled, &latency, &speed, &item.LastStatus, &lastTestAt, &lastSpeedAt, &item.LastError, &item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return models.SubscriptionNode{}, err
	}
	item.Enabled = enabled == 1
	if latency.Valid {
		v := latency.Int64
		item.LastLatencyMS = &v
	}
	if speed.Valid {
		v := speed.Float64
		item.LastSpeedMbps = &v
	}
	if lastTestAt.Valid {
		v := lastTestAt.Time
		item.LastTestAt = &v
	}
	if lastSpeedAt.Valid {
		v := lastSpeedAt.Time
		item.LastSpeedAt = &v
	}
	return item, nil
}

func parseHeaders(raw string) map[string]string {
	var headers map[string]string
	_ = json.Unmarshal([]byte(defaultJSON(raw)), &headers)
	return headers
}

func normalizeHeadersJSON(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}", nil
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", fmt.Errorf("headers_json must be a JSON object with string values")
	}
	if payload == nil && raw != "{}" {
		return "", fmt.Errorf("headers_json must be a JSON object with string values")
	}

	headers := make(map[string]string, len(payload))
	for key, value := range payload {
		var headerValue string
		if err := json.Unmarshal(value, &headerValue); err != nil {
			return "", fmt.Errorf("headers_json must be a JSON object with string values")
		}
		headers[key] = headerValue
	}

	data, err := json.Marshal(headers)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func defaultJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}

func (s *Service) normalizeUpsertRequest(ctx context.Context, req UpsertRequest) (UpsertRequest, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)

	headersJSON, err := normalizeHeadersJSON(req.HeadersJSON)
	if err != nil {
		return UpsertRequest{}, err
	}
	req.HeadersJSON = headersJSON

	if req.SyncIntervalSec <= 0 {
		st, err := s.settingsSvc.Get(ctx)
		if err != nil {
			return UpsertRequest{}, err
		}
		req.SyncIntervalSec = st.DefaultSubscriptionIntervalSec
	}
	return req, nil
}

func loadStoredSubscriptionNodes(ctx context.Context, tx *sql.Tx, subscriptionID int64) ([]storedSubscriptionNode, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, display_name, protocol, server, port, raw_payload, normalized_json
		FROM subscription_nodes WHERE subscription_id = ? ORDER BY id ASC`, subscriptionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []storedSubscriptionNode
	for rows.Next() {
		var item storedSubscriptionNode
		if err := rows.Scan(&item.ID, &item.DisplayName, &item.Protocol, &item.Server, &item.Port, &item.RawPayload, &item.NormalizedJSON); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func subscriptionNodeFingerprint(protocol, server string, port int, normalizedJSON string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(normalizedJSON)), &payload); err != nil || payload == nil {
		payload = make(map[string]any)
	}
	if protocol != "" {
		payload["type"] = protocol
	}
	if server != "" {
		payload["server"] = server
	}
	if port > 0 {
		payload["port"] = port
	}
	delete(payload, "name")
	fingerprint, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("%s|%s|%d|%s", protocol, server, port, strings.TrimSpace(normalizedJSON))
	}
	return string(fingerprint)
}

func popStoredSubscriptionNode(items []storedSubscriptionNode, rawPayload string) (storedSubscriptionNode, bool) {
	if len(items) == 0 {
		return storedSubscriptionNode{}, false
	}
	for _, item := range items {
		if item.RawPayload == rawPayload {
			return item, true
		}
	}
	return items[0], true
}

func removeStoredSubscriptionNode(items []storedSubscriptionNode, id int64) []storedSubscriptionNode {
	for index, item := range items {
		if item.ID != id {
			continue
		}
		return append(items[:index], items[index+1:]...)
	}
	return items
}

func needsStoredSubscriptionNodeUpdate(existing storedSubscriptionNode, next nodes.ParsedNode, normalizedJSON string) bool {
	return existing.DisplayName != next.DisplayName ||
		existing.Protocol != next.Protocol ||
		existing.Server != next.Server ||
		existing.Port != next.Port ||
		existing.RawPayload != next.RawPayload ||
		existing.NormalizedJSON != normalizedJSON
}

func (s *Service) doWithRetry(req *http.Request, retryCount int) (*http.Response, error) {
	attempts := retryCount + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		cloned := req.Clone(req.Context())
		resp, err := s.client.Do(cloned)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt < attempts-1 {
			time.Sleep(time.Duration(attempt+1) * 300 * time.Millisecond)
		}
	}
	return nil, lastErr
}

func (s *Service) runDueSyncs(ctx context.Context) {
	items, err := s.List(ctx)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, item := range items {
		if !shouldSyncSubscription(item, now) {
			continue
		}
		id := item.ID
		go func() {
			_, _ = s.Sync(ctx, id)
		}()
	}
}

func shouldSyncSubscription(item models.Subscription, now time.Time) bool {
	if !item.Enabled {
		return false
	}
	if item.SyncIntervalSec <= 0 {
		return false
	}
	if item.LastSyncAt == nil {
		return true
	}
	return !item.LastSyncAt.Add(time.Duration(item.SyncIntervalSec) * time.Second).After(now)
}

func (s *Service) beginSync(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.syncing[id]; exists {
		return false
	}
	s.syncing[id] = struct{}{}
	return true
}

func (s *Service) endSync(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.syncing, id)
}

func errorSummary(errs []error) string {
	if len(errs) == 0 {
		return ""
	}
	return errs[0].Error()
}

func stringifyErrors(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return out
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
