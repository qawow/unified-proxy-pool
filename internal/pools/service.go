package pools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/events"
	"unified-proxy-pool/internal/freproxies"
	"unified-proxy-pool/internal/mihomo"
	"unified-proxy-pool/internal/models"
	"unified-proxy-pool/internal/nodes"
	"unified-proxy-pool/internal/settings"
	"unified-proxy-pool/internal/subscriptions"
)

type Service struct {
	store         *db.Store
	settingsSvc   *settings.Service
	manualNodes   *nodes.Service
	subscriptions *subscriptions.Service
	free          *freproxies.Service
	mihomo        *mihomo.Manager
	events        *events.Broker
}

type UpsertRequest struct {
	Name                 string `json:"name"`
	AuthUsername         string `json:"auth_username"`
	AuthPasswordSecret   string `json:"auth_password_secret"`
	Strategy             string `json:"strategy"`
	StrategyLabel        string `json:"strategy_label"`
	StrategyAdvancedJSON string `json:"strategy_advanced_json"`
	FailoverEnabled      bool   `json:"failover_enabled"`
	Enabled              bool   `json:"enabled"`
}

type MemberInput struct {
	SourceType   string `json:"source_type"`
	SourceNodeID int64  `json:"source_node_id"`
	Enabled      bool   `json:"enabled"`
	Weight       int    `json:"weight"`
}

const maxMemberWeight = 32

func NewService(store *db.Store, settingsSvc *settings.Service, manualNodes *nodes.Service, subscriptions *subscriptions.Service, mihomoMgr *mihomo.Manager, broker *events.Broker) *Service {
	return &Service{
		store:         store,
		settingsSvc:   settingsSvc,
		manualNodes:   manualNodes,
		subscriptions: subscriptions,
		mihomo:        mihomoMgr,
		events:        broker,
	}
}

func (s *Service) SetFreeService(free *freproxies.Service) {
	s.free = free
}

func (s *Service) List(ctx context.Context) ([]models.ProxyPool, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT p.id, p.name, p.auth_username,
		p.auth_password_secret, p.strategy, COALESCE(p.strategy_label,''), COALESCE(p.strategy_advanced_json,'{}'),
		p.failover_enabled, p.enabled, p.last_published_at, p.last_publish_status, p.last_error,
		p.created_at, p.updated_at, COUNT(m.id) AS member_count,
		SUM(CASE WHEN ((m.source_type='manual' AND mn.last_status='available') OR (m.source_type='subscription' AND sn.last_status='available') OR (m.source_type='free_proxy')) THEN 1 ELSE 0 END) AS healthy_count
		FROM proxy_pools p
		LEFT JOIN proxy_pool_members m ON p.id = m.pool_id AND m.enabled = 1
		LEFT JOIN manual_nodes mn ON m.source_type='manual' AND m.source_node_id = mn.id
		LEFT JOIN subscription_nodes sn ON m.source_type='subscription' AND m.source_node_id = sn.id
		GROUP BY p.id
		ORDER BY p.updated_at DESC, p.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []models.ProxyPool
	for rows.Next() {
		item, err := scanPool(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) Get(ctx context.Context, id int64) (models.ProxyPool, error) {
	row := s.store.DB.QueryRowContext(ctx, `SELECT id, name, auth_username,
		auth_password_secret, strategy, COALESCE(strategy_label,''), COALESCE(strategy_advanced_json,'{}'),
		failover_enabled, enabled, last_published_at, last_publish_status, last_error,
		created_at, updated_at, 0, 0 FROM proxy_pools WHERE id = ?`, id)
	return scanPool(row)
}

func (s *Service) Create(ctx context.Context, req UpsertRequest) (models.ProxyPool, error) {
	req = normalizeUpsertRequest(req)
	if err := s.validateUpsertRequest(ctx, 0, req); err != nil {
		return models.ProxyPool{}, err
	}
	now := time.Now().UTC()
	res, err := s.store.DB.ExecContext(ctx, `INSERT INTO proxy_pools (
		name, auth_username, auth_password_secret,
		strategy, strategy_label, strategy_advanced_json, failover_enabled, enabled, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.Name, req.AuthUsername, req.AuthPasswordSecret,
		defaultStrategy(req.Strategy), req.StrategyLabel, req.StrategyAdvancedJSON, boolToInt(req.FailoverEnabled),
		boolToInt(req.Enabled), now, now,
	)
	if err != nil {
		return models.ProxyPool{}, err
	}
	id, _ := res.LastInsertId()
	item, err := s.Get(ctx, id)
	if err == nil {
		s.events.Publish("pools.created", item)
	}
	return item, err
}

func (s *Service) Update(ctx context.Context, id int64, req UpsertRequest) (models.ProxyPool, error) {
	req = normalizeUpsertRequest(req)
	existing, err := s.Get(ctx, id)
	if err != nil {
		return models.ProxyPool{}, err
	}
	if req.AuthPasswordSecret == "" {
		req.AuthPasswordSecret = existing.AuthPasswordSecret
	}
	if err := s.validateUpsertRequest(ctx, id, req); err != nil {
		return models.ProxyPool{}, err
	}
	_, err = s.store.DB.ExecContext(ctx, `UPDATE proxy_pools SET name = ?,
		auth_username = ?, auth_password_secret = ?, strategy = ?, strategy_label = ?, strategy_advanced_json = ?,
		failover_enabled = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		req.Name,
		req.AuthUsername, req.AuthPasswordSecret, defaultStrategy(req.Strategy), req.StrategyLabel, req.StrategyAdvancedJSON,
		boolToInt(req.FailoverEnabled), boolToInt(req.Enabled),
		time.Now().UTC(), id,
	)
	if err != nil {
		return models.ProxyPool{}, err
	}
	item, err := s.Get(ctx, id)
	if err == nil {
		s.events.Publish("pools.updated", item)
	}
	return item, err
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	_, err := s.store.DB.ExecContext(ctx, `DELETE FROM proxy_pools WHERE id = ?`, id)
	if err == nil {
		s.events.Publish("pools.deleted", map[string]int64{"id": id})
	}
	return err
}

func (s *Service) Toggle(ctx context.Context, id int64) (models.ProxyPool, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return models.ProxyPool{}, err
	}
	_, err = s.store.DB.ExecContext(ctx, `UPDATE proxy_pools SET enabled = ?, updated_at = ? WHERE id = ?`,
		boolToInt(!current.Enabled), time.Now().UTC(), id)
	if err != nil {
		return models.ProxyPool{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) GetMembers(ctx context.Context, poolID int64) ([]models.ProxyPoolMember, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id, pool_id, source_type, source_node_id, enabled, weight, created_at, updated_at
		FROM proxy_pool_members WHERE pool_id = ? ORDER BY id ASC`, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.ProxyPoolMember
	for rows.Next() {
		var item models.ProxyPoolMember
		var enabled int
		if err := rows.Scan(&item.ID, &item.PoolID, &item.SourceType, &item.SourceNodeID, &enabled, &item.Weight, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) UpdateMembers(ctx context.Context, poolID int64, members []MemberInput) error {
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM proxy_pool_members WHERE pool_id = ?`, poolID); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, item := range members {
		if item.SourceType == "" || item.SourceNodeID == 0 {
			continue
		}
		item.Weight = normalizedMemberWeight(item.Weight)
		if _, err := tx.ExecContext(ctx, `INSERT INTO proxy_pool_members (pool_id, source_type, source_node_id, enabled, weight, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, poolID, item.SourceType, item.SourceNodeID, boolToInt(item.Enabled), item.Weight, now, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.events.Publish("pools.members.updated", map[string]any{"pool_id": poolID})
	return nil
}

func (s *Service) AvailableCandidates(ctx context.Context) ([]models.PoolMemberView, error) {
	manual, err := s.manualNodes.ListPoolCandidates(ctx)
	if err != nil {
		return nil, err
	}
	subs, err := s.subscriptions.ListPoolCandidates(ctx)
	if err != nil {
		return nil, err
	}
	out := append(manual, subs...)
	if s.free != nil {
		freeItems, err := s.free.ListPoolCandidates(ctx, 100)
		if err == nil {
			out = append(out, freeItems...)
		}
	}
	return out, nil
}

func (s *Service) Publish(ctx context.Context, poolID int64) error {
	s.events.Publish("pools.publish.started", map[string]any{"pool_id": poolID})
	settingsRow, err := s.settingsSvc.Get(ctx)
	if err != nil {
		s.markPublishFailure(ctx, poolID, err)
		return err
	}
	poolList, err := s.List(ctx)
	if err != nil {
		s.markPublishFailure(ctx, poolID, err)
		return err
	}
	manualInventory, err := s.manualNodes.AllRuntimeNodes(ctx)
	if err != nil {
		s.markPublishFailure(ctx, poolID, err)
		return err
	}
	subscriptionInventory, err := s.subscriptions.AllRuntimeNodes(ctx)
	if err != nil {
		s.markPublishFailure(ctx, poolID, err)
		return err
	}
	inventory := append(manualInventory, subscriptionInventory...)
	if s.free != nil {
		if freeInv, err := s.free.AllRuntimeNodes(ctx, 100); err == nil {
			inventory = append(inventory, freeInv...)
		}
	}

	members := make(map[int64][]models.RuntimeNode)
	for _, pool := range poolList {
		currentMembers, err := s.runtimeMembersForPool(ctx, pool)
		if err != nil {
			s.markPublishFailure(ctx, poolID, err)
			return err
		}
		members[pool.ID] = currentMembers
	}

	bundle, err := BuildPublishBundle(
		settingsRow.MihomoControllerSecret,
		s.mihomo.ProdControllerAddr(),
		s.mihomo.ProbeControllerAddr(),
		s.mihomo.ProbeMixedPort(),
		settingsRow.LatencyTestURL,
		settingsRow.LogLevel,
		poolList,
		members,
		inventory,
	)
	if err != nil {
		s.markPublishFailure(ctx, poolID, err)
		return err
	}
	if err := s.mihomo.ApplyConfigBundle(ctx, bundle.ProdConfig, bundle.ProbeConfig, settingsRow.MihomoControllerSecret); err != nil {
		s.markPublishFailure(ctx, poolID, err)
		return err
	}
	if err := s.markPublishSuccess(ctx, poolID, poolList); err != nil {
		return err
	}
	s.events.Publish("pools.published", map[string]string{"status": "published"})
	return nil
}

func (s *Service) validateUpsertRequest(ctx context.Context, currentID int64, req UpsertRequest) error {
	req = normalizeUpsertRequest(req)
	if req.Name == "" {
		return fmt.Errorf("pool name is required")
	}
	if req.StrategyAdvancedJSON != "" && req.StrategyAdvancedJSON != "{}" {
		var tmp map[string]any
		if err := json.Unmarshal([]byte(req.StrategyAdvancedJSON), &tmp); err != nil {
			return fmt.Errorf("strategy_advanced_json must be valid JSON object")
		}
	}
	if req.AuthUsername == "" {
		return fmt.Errorf("auth_username is required (used to identify the pool)")
	}
	if req.AuthPasswordSecret == "" {
		return fmt.Errorf("auth_password_secret is required")
	}
	if err := s.validateUniqueUsername(ctx, currentID, req.AuthUsername); err != nil {
		return err
	}
	return nil
}

func (s *Service) validateUniqueUsername(ctx context.Context, currentID int64, username string) error {
	username = normalizeCredential(username)
	var count int
	err := s.store.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM proxy_pools WHERE auth_username = ? AND id != ?`, username, currentID,
	).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("username %q is already used by another pool", username)
	}
	return nil
}

// LookupPoolByAuth finds an enabled pool by username and password.
func (s *Service) LookupPoolByAuth(ctx context.Context, username, password string) (*models.ProxyPool, error) {
	username = normalizeCredential(username)
	password = normalizeCredential(password)
	if username == "" || password == "" {
		return nil, nil
	}
	row := s.store.DB.QueryRowContext(ctx, `SELECT id, name, auth_username,
		auth_password_secret, strategy, COALESCE(strategy_label,''), COALESCE(strategy_advanced_json,'{}'),
		failover_enabled, enabled, last_published_at, last_publish_status, last_error,
		created_at, updated_at, 0, 0 FROM proxy_pools WHERE enabled = 1 AND auth_username = ? AND auth_password_secret = ? LIMIT 1`,
		username, password)
	item, err := scanPool(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Service) runtimeMembersForPool(ctx context.Context, pool models.ProxyPool) ([]models.RuntimeNode, error) {
	memberRows, err := s.store.DB.QueryContext(ctx, `SELECT source_type, source_node_id, enabled, weight FROM proxy_pool_members WHERE pool_id = ?`, pool.ID)
	if err != nil {
		return nil, err
	}
	type memberRef struct {
		SourceType   string
		SourceNodeID int64
		Enabled      bool
		Weight       int
	}
	var refs []memberRef
	for memberRows.Next() {
		var ref memberRef
		var enabled int
		if err := memberRows.Scan(&ref.SourceType, &ref.SourceNodeID, &enabled, &ref.Weight); err != nil {
			memberRows.Close()
			return nil, err
		}
		ref.Enabled = enabled == 1
		ref.Weight = normalizedMemberWeight(ref.Weight)
		refs = append(refs, ref)
	}
	if err := memberRows.Close(); err != nil {
		return nil, err
	}
	if err := memberRows.Err(); err != nil {
		return nil, err
	}

	var result []models.RuntimeNode
	for _, ref := range refs {
		if !ref.Enabled {
			continue
		}
		copies := memberCopiesForStrategy(pool.Strategy, ref.Weight)
		switch ref.SourceType {
		case "manual":
			node, err := s.manualNodes.NodeBySource(ctx, ref.SourceNodeID)
			if err == nil {
				for range copies {
					result = append(result, node)
				}
			}
		case freproxies.SourceTypeFree:
			if s.free == nil {
				continue
			}
			node, err := s.free.RuntimeNodeByID(ctx, ref.SourceNodeID)
			if err == nil {
				for range copies {
					result = append(result, node)
				}
			}
		default:
			node, err := s.subscriptions.NodeBySource(ctx, ref.SourceNodeID)
			if err == nil {
				for range copies {
					result = append(result, node)
				}
			}
		}
	}
	return result, nil
}

func normalizedMemberWeight(weight int) int {
	if weight < 1 {
		return 1
	}
	if weight > maxMemberWeight {
		return maxMemberWeight
	}
	return weight
}

func memberCopiesForStrategy(strategy string, weight int) int {
	switch defaultStrategy(strategy) {
	case "round_robin", "sticky":
		return normalizedMemberWeight(weight)
	default:
		return 1
	}
}

func (s *Service) markPublishSuccess(ctx context.Context, poolID int64, poolList []models.ProxyPool) error {
	now := time.Now().UTC()
	if poolID != 0 {
		_, err := s.store.DB.ExecContext(ctx, `UPDATE proxy_pools
			SET last_published_at = ?, last_publish_status = ?, last_error = ?, updated_at = ?
			WHERE id = ?`,
			now, "published", "", now, poolID,
		)
		return err
	}
	for _, pool := range poolList {
		if _, err := s.store.DB.ExecContext(ctx, `UPDATE proxy_pools
			SET last_published_at = ?, last_publish_status = ?, last_error = ?, updated_at = ?
			WHERE id = ?`,
			now, "published", "", now, pool.ID,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) markPublishFailure(ctx context.Context, poolID int64, cause error) {
	now := time.Now().UTC()
	if poolID == 0 {
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE proxy_pools SET last_publish_status = ?, last_error = ?, updated_at = ?`,
			"failed", cause.Error(), now)
	} else {
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE proxy_pools SET last_publish_status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
			"failed", cause.Error(), now, poolID)
	}
	s.events.Publish("pools.publish.failed", map[string]string{"error": cause.Error()})
}

func scanPool(scanner interface{ Scan(dest ...any) error }) (models.ProxyPool, error) {
	var item models.ProxyPool
	var failoverEnabled int
	var enabled int
	var lastPublishedAt sql.NullTime
	var healthy sql.NullInt64
	err := scanner.Scan(
		&item.ID, &item.Name, &item.AuthUsername,
		&item.AuthPasswordSecret, &item.Strategy, &item.StrategyLabel, &item.StrategyAdvancedJSON,
		&failoverEnabled, &enabled, &lastPublishedAt, &item.LastPublishStatus,
		&item.LastError, &item.CreatedAt, &item.UpdatedAt, &item.CurrentMemberCount, &healthy,
	)
	if err != nil {
		return models.ProxyPool{}, err
	}
	item.FailoverEnabled = failoverEnabled == 1
	item.Enabled = enabled == 1
	if item.StrategyAdvancedJSON == "" {
		item.StrategyAdvancedJSON = "{}"
	}
	if lastPublishedAt.Valid {
		v := lastPublishedAt.Time
		item.LastPublishedAt = &v
	}
	if healthy.Valid {
		item.CurrentHealthyCount = int(healthy.Int64)
	}
	return item, nil
}

func defaultStrategy(v string) string {
	switch v {
	case "lowest_latency", "failover", "sticky":
		return v
	default:
		return "round_robin"
	}
}

func normalizeUpsertRequest(req UpsertRequest) UpsertRequest {
	req.Name = strings.TrimSpace(req.Name)
	req.AuthUsername = normalizeCredential(req.AuthUsername)
	req.AuthPasswordSecret = normalizeCredential(req.AuthPasswordSecret)
	req.Strategy = strings.TrimSpace(req.Strategy)
	req.StrategyLabel = strings.TrimSpace(req.StrategyLabel)
	req.StrategyAdvancedJSON = strings.TrimSpace(req.StrategyAdvancedJSON)
	if req.StrategyAdvancedJSON == "" {
		req.StrategyAdvancedJSON = "{}"
	}
	// validate JSON
	if req.StrategyAdvancedJSON != "{}" {
		var tmp map[string]any
		if err := json.Unmarshal([]byte(req.StrategyAdvancedJSON), &tmp); err != nil {
			// keep raw but normalize to empty object on invalid — validation catches later
			_ = err
		}
	}
	return req
}

func normalizeCredential(value string) string {
	return strings.TrimSpace(value)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
