package nodes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/events"
	"unified-proxy-pool/internal/geoip"
	"unified-proxy-pool/internal/models"
)

type Service struct {
	store  *db.Store
	events *events.Broker
}

type CreateRequest struct {
	Content string `json:"content"`
}

type UpdateRequest struct {
	DisplayName string `json:"display_name"`
	RawPayload  string `json:"raw_payload"`
	Enabled     *bool  `json:"enabled"`
}

func NewService(store *db.Store, broker *events.Broker) *Service {
	return &Service{store: store, events: broker}
}

func (s *Service) List(ctx context.Context) ([]models.ManualNode, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id, display_name, protocol, server, port, raw_payload, normalized_json,
		enabled, last_latency_ms, last_speed_mbps, last_status, last_test_at, last_speed_at, last_error, created_at, updated_at
		FROM manual_nodes ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []models.ManualNode
	for rows.Next() {
		item, err := scanManualNode(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) Get(ctx context.Context, id int64) (models.ManualNode, error) {
	row := s.store.DB.QueryRowContext(ctx, `SELECT id, display_name, protocol, server, port, raw_payload, normalized_json,
		enabled, last_latency_ms, last_speed_mbps, last_status, last_test_at, last_speed_at, last_error, created_at, updated_at
		FROM manual_nodes WHERE id = ?`, id)
	return scanManualNode(row)
}

func (s *Service) Create(ctx context.Context, req CreateRequest) ([]models.ManualNode, []error, error) {
	parsed, parseErrs := ParseRawNodes(req.Content)
	if len(parsed) == 0 {
		return nil, parseErrs, errors.New("no nodes created")
	}
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	var created []models.ManualNode
	for _, item := range parsed {
		if geoip.Active().BlockedNode(item.Server, item.DisplayName) {
			parseErrs = append(parseErrs, fmt.Errorf("%s: blocked country", item.DisplayName))
			continue
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO manual_nodes (
			display_name, protocol, server, port, raw_payload, normalized_json, enabled, last_status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 1, 'unknown', ?, ?)`,
			item.DisplayName, item.Protocol, item.Server, item.Port, item.RawPayload, NormalizeJSON(item.Normalized), now, now,
		)
		if err != nil {
			return nil, parseErrs, err
		}
		id, _ := res.LastInsertId()
		created = append(created, models.ManualNode{
			ID:             id,
			DisplayName:    item.DisplayName,
			Protocol:       item.Protocol,
			Server:         item.Server,
			Port:           item.Port,
			RawPayload:     item.RawPayload,
			NormalizedJSON: NormalizeJSON(item.Normalized),
			Enabled:        true,
			LastStatus:     "unknown",
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	}
	if len(created) == 0 {
		return nil, parseErrs, errors.New("no nodes created")
	}
	if err := tx.Commit(); err != nil {
		return nil, parseErrs, err
	}
	s.events.Publish("manual_nodes.created", created)
	return created, parseErrs, nil
}

func (s *Service) Update(ctx context.Context, id int64, req UpdateRequest) (models.ManualNode, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return models.ManualNode{}, err
	}
	enabled := current.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	displayName := current.DisplayName
	protocol := current.Protocol
	server := current.Server
	port := current.Port
	rawPayload := current.RawPayload
	normalizedJSON := current.NormalizedJSON

	if trimmed := strings.TrimSpace(req.RawPayload); trimmed != "" && trimmed != strings.TrimSpace(current.RawPayload) {
		parsed, errs := ParseRawNodes(trimmed)
		if len(parsed) == 0 {
			if len(errs) > 0 {
				return models.ManualNode{}, errs[0]
			}
			return models.ManualNode{}, errors.New("payload parse failed")
		}
		node := parsed[0]
		displayName = node.DisplayName
		protocol = node.Protocol
		server = node.Server
		port = node.Port
		rawPayload = node.RawPayload
		normalizedJSON = NormalizeJSON(node.Normalized)
	} else if strings.TrimSpace(req.DisplayName) != "" {
		displayName = strings.TrimSpace(req.DisplayName)
	}

	_, err = s.store.DB.ExecContext(ctx, `UPDATE manual_nodes SET display_name = ?, protocol = ?, server = ?, port = ?,
		raw_payload = ?, normalized_json = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		displayName, protocol, server, port, rawPayload, normalizedJSON, boolToInt(enabled), time.Now().UTC(), id)
	if err != nil {
		return models.ManualNode{}, err
	}
	updated, err := s.Get(ctx, id)
	if err == nil {
		s.events.Publish("manual_nodes.updated", updated)
	}
	return updated, err
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	_, err := s.store.DB.ExecContext(ctx, `DELETE FROM manual_nodes WHERE id = ?`, id)
	if err == nil {
		s.events.Publish("manual_nodes.deleted", map[string]int64{"id": id})
	}
	return err
}

func (s *Service) Toggle(ctx context.Context, id int64) (models.ManualNode, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return models.ManualNode{}, err
	}
	_, err = s.store.DB.ExecContext(ctx, `UPDATE manual_nodes SET enabled = ?, updated_at = ? WHERE id = ?`,
		boolToInt(!current.Enabled), time.Now().UTC(), id)
	if err != nil {
		return models.ManualNode{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) AllRuntimeNodes(ctx context.Context) ([]models.RuntimeNode, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id, display_name, protocol, server, port, raw_payload, normalized_json, enabled, last_status FROM manual_nodes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.RuntimeNode
	for rows.Next() {
		var item models.RuntimeNode
		item.SourceType = "manual"
		if err := rows.Scan(&item.SourceNodeID, &item.DisplayName, &item.Protocol, &item.Server, &item.Port, &item.RawPayload, &item.NormalizedJSON, &item.Enabled, &item.LastStatus); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) ListPoolCandidates(ctx context.Context) ([]models.PoolMemberView, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id, display_name, protocol, server, port, enabled, last_status, last_latency_ms, last_speed_mbps
		FROM manual_nodes ORDER BY display_name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.PoolMemberView
	for rows.Next() {
		var item models.PoolMemberView
		item.SourceType = "manual"
		item.SourceLabel = "手动节点"
		if err := rows.Scan(&item.SourceNodeID, &item.DisplayName, &item.Protocol, &item.Server, &item.Port, &item.Enabled, &item.LastStatus, &item.LastLatencyMS, &item.LastSpeedMbps); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) NodeBySource(ctx context.Context, id int64) (models.RuntimeNode, error) {
	row := s.store.DB.QueryRowContext(ctx, `SELECT id, display_name, protocol, server, port, raw_payload, normalized_json, enabled, last_status
		FROM manual_nodes WHERE id = ?`, id)
	var item models.RuntimeNode
	item.SourceType = "manual"
	err := row.Scan(&item.SourceNodeID, &item.DisplayName, &item.Protocol, &item.Server, &item.Port, &item.RawPayload, &item.NormalizedJSON, &item.Enabled, &item.LastStatus)
	return item, err
}

func (s *Service) UpdateProbeResult(ctx context.Context, sourceNodeID int64, latency *int64, speed *float64, status, errMsg string, isSpeed bool) error {
	now := time.Now().UTC()
	if isSpeed {
		_, err := s.store.DB.ExecContext(ctx, `UPDATE manual_nodes SET last_speed_mbps = ?, last_speed_at = ?, last_status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
			speed, now, status, errMsg, now, sourceNodeID)
		return err
	}
	_, err := s.store.DB.ExecContext(ctx, `UPDATE manual_nodes SET last_latency_ms = ?, last_test_at = ?, last_status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		latency, now, status, errMsg, now, sourceNodeID)
	return err
}

func (s *Service) SetTransientStatus(ctx context.Context, sourceNodeID int64, status, errMsg string) error {
	_, err := s.store.DB.ExecContext(ctx, `UPDATE manual_nodes SET last_status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		status, errMsg, time.Now().UTC(), sourceNodeID)
	return err
}

func scanManualNode(scanner interface{ Scan(dest ...any) error }) (models.ManualNode, error) {
	var item models.ManualNode
	var enabled int
	var latency sql.NullInt64
	var speed sql.NullFloat64
	var lastTestAt sql.NullTime
	var lastSpeedAt sql.NullTime
	err := scanner.Scan(
		&item.ID, &item.DisplayName, &item.Protocol, &item.Server, &item.Port, &item.RawPayload, &item.NormalizedJSON,
		&enabled, &latency, &speed, &item.LastStatus, &lastTestAt, &lastSpeedAt, &item.LastError, &item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return models.ManualNode{}, err
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

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// DisableBlocked permanently deletes manual nodes the country filter rejects.
func (s *Service) DisableBlocked(ctx context.Context) (int, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	items, err := s.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, item := range items {
		if !geoip.Active().BlockedNode(item.Server, item.DisplayName) {
			continue
		}
		if _, err := s.store.DB.ExecContext(ctx, `DELETE FROM manual_nodes WHERE id = ?`, item.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
