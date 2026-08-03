package scrapers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/db"
)

type Record struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	URLs       []string `json:"urls"`
	Format     string   `json:"format"`
	Protocol   string   `json:"protocol"`
	Enabled    bool     `json:"enabled"`
	Fragile    bool     `json:"fragile"`
	HostCol    int      `json:"host_col"`
	PortCol    int      `json:"port_col"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Builtin    bool     `json:"builtin"`
}

type Service struct {
	store    *db.Store
	registry *crawlers.Registry
}

func New(store *db.Store, registry *crawlers.Registry) *Service {
	s := &Service{store: store, registry: registry}
	s.LoadAll(context.Background())
	return s
}

func (s *Service) LoadAll(ctx context.Context) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id, name, urls_json, format, protocol, enabled, fragile, parse_options_json, created_at, updated_at FROM custom_scrapers ORDER BY id`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			continue
		}
		c, err := crawlers.NewDynamic(toSpec(rec))
		if err != nil {
			continue
		}
		s.registry.RegisterDynamic(c)
	}
}

func (s *Service) ListCustom(ctx context.Context) ([]Record, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id, name, urls_json, format, protocol, enabled, fragile, parse_options_json, created_at, updated_at FROM custom_scrapers ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		rec.Builtin = false
		out = append(out, rec)
	}
	if out == nil {
		out = []Record{}
	}
	return out, rows.Err()
}

type UpsertRequest struct {
	Name     string   `json:"name"`
	URLs     []string `json:"urls"`
	Format   string   `json:"format"`
	Protocol string   `json:"protocol"`
	Enabled  bool     `json:"enabled"`
	Fragile  bool     `json:"fragile"`
	HostCol  int      `json:"host_col"`
	PortCol  int      `json:"port_col"`
}

func (s *Service) Create(ctx context.Context, req UpsertRequest) (Record, error) {
	req = normalize(req)
	if _, exists := s.registry.Get(req.Name); exists && crawlers.IsBuiltin(mustGet(s.registry, req.Name)) {
		return Record{}, fmt.Errorf("name conflicts with builtin scraper")
	}
	c, err := crawlers.NewDynamic(toSpec(Record{
		Name: req.Name, URLs: req.URLs, Format: req.Format, Protocol: req.Protocol,
		Enabled: req.Enabled, Fragile: req.Fragile, HostCol: req.HostCol, PortCol: req.PortCol,
	}))
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	urlsJSON, _ := json.Marshal(req.URLs)
	opt, _ := json.Marshal(map[string]int{"host_col": req.HostCol, "port_col": req.PortCol})
	res, err := s.store.DB.ExecContext(ctx, `INSERT INTO custom_scrapers(name, urls_json, format, protocol, enabled, fragile, parse_options_json, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		req.Name, string(urlsJSON), req.Format, req.Protocol, boolInt(req.Enabled), boolInt(req.Fragile), string(opt), now, now)
	if err != nil {
		return Record{}, err
	}
	id, _ := res.LastInsertId()
	s.registry.RegisterDynamic(c)
	return s.Get(ctx, id)
}

func (s *Service) Update(ctx context.Context, name string, req UpsertRequest) (Record, error) {
	req = normalize(req)
	req.Name = name
	existing, err := s.getByName(ctx, name)
	if err != nil {
		return Record{}, err
	}
	c, err := crawlers.NewDynamic(toSpec(Record{
		Name: name, URLs: req.URLs, Format: req.Format, Protocol: req.Protocol,
		Enabled: req.Enabled, Fragile: req.Fragile, HostCol: req.HostCol, PortCol: req.PortCol,
	}))
	if err != nil {
		return Record{}, err
	}
	urlsJSON, _ := json.Marshal(req.URLs)
	opt, _ := json.Marshal(map[string]int{"host_col": req.HostCol, "port_col": req.PortCol})
	_, err = s.store.DB.ExecContext(ctx, `UPDATE custom_scrapers SET urls_json=?, format=?, protocol=?, enabled=?, fragile=?, parse_options_json=?, updated_at=? WHERE name=?`,
		string(urlsJSON), req.Format, req.Protocol, boolInt(req.Enabled), boolInt(req.Fragile), string(opt), time.Now().UTC(), name)
	if err != nil {
		return Record{}, err
	}
	s.registry.RegisterDynamic(c)
	return s.Get(ctx, existing.ID)
}

func (s *Service) Delete(ctx context.Context, name string) error {
	if c, ok := s.registry.Get(name); ok && crawlers.IsBuiltin(c) {
		return fmt.Errorf("cannot delete builtin scraper")
	}
	res, err := s.store.DB.ExecContext(ctx, `DELETE FROM custom_scrapers WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("scraper not found")
	}
	s.registry.Remove(name)
	return nil
}

func (s *Service) Get(ctx context.Context, id int64) (Record, error) {
	row := s.store.DB.QueryRowContext(ctx, `SELECT id, name, urls_json, format, protocol, enabled, fragile, parse_options_json, created_at, updated_at FROM custom_scrapers WHERE id = ?`, id)
	rec, err := scanRecord(row)
	if err != nil {
		return Record{}, err
	}
	rec.Builtin = false
	return rec, nil
}

func (s *Service) getByName(ctx context.Context, name string) (Record, error) {
	row := s.store.DB.QueryRowContext(ctx, `SELECT id, name, urls_json, format, protocol, enabled, fragile, parse_options_json, created_at, updated_at FROM custom_scrapers WHERE name = ?`, name)
	return scanRecord(row)
}

func mustGet(r *crawlers.Registry, name string) crawlers.Crawler {
	c, _ := r.Get(name)
	return c
}

func normalize(req UpsertRequest) UpsertRequest {
	req.Name = strings.TrimSpace(req.Name)
	req.Format = strings.ToLower(strings.TrimSpace(req.Format))
	req.Protocol = strings.ToLower(strings.TrimSpace(req.Protocol))
	clean := req.URLs[:0]
	for _, u := range req.URLs {
		u = strings.TrimSpace(u)
		if u != "" {
			clean = append(clean, u)
		}
	}
	req.URLs = clean
	return req
}

func toSpec(rec Record) crawlers.DynamicSpec {
	return crawlers.DynamicSpec{
		Name: rec.Name, URLs: rec.URLs, Format: rec.Format, Protocol: rec.Protocol,
		Enabled: rec.Enabled, Fragile: rec.Fragile, HostCol: rec.HostCol, PortCol: rec.PortCol,
	}
}

func scanRecord(scanner interface{ Scan(dest ...any) error }) (Record, error) {
	var rec Record
	var urlsJSON, optJSON string
	var enabled, fragile int
	err := scanner.Scan(&rec.ID, &rec.Name, &urlsJSON, &rec.Format, &rec.Protocol, &enabled, &fragile, &optJSON, &rec.CreatedAt, &rec.UpdatedAt)
	if err != nil {
		return Record{}, err
	}
	_ = json.Unmarshal([]byte(urlsJSON), &rec.URLs)
	if rec.URLs == nil {
		rec.URLs = []string{}
	}
	var opt map[string]int
	_ = json.Unmarshal([]byte(optJSON), &opt)
	if opt != nil {
		rec.HostCol = opt["host_col"]
		rec.PortCol = opt["port_col"]
	}
	rec.Enabled = enabled == 1
	rec.Fragile = fragile == 1
	return rec, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

var _ = sql.ErrNoRows
