package freproxies

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// groupNameRe restricts group names to safe, URL-friendly identifiers.
var groupNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,39}$`)

// NormalizeGroupName lowercases and validates a group name.
func NormalizeGroupName(name string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if !groupNameRe.MatchString(n) {
		return "", fmt.Errorf("分组名只能包含字母、数字、下划线和连字符，长度 1-40")
	}
	return n, nil
}

// NormalizeGroupRule trims and validates every dimension of a rule.
func NormalizeGroupRule(r GroupRule) (GroupRule, error) {
	out := GroupRule{MinScore: r.MinScore, OnlyOK: r.OnlyOK}
	out.Sources = dedupeTrim(r.Sources)
	out.Protocols = dedupeTrimLower(r.Protocols)
	out.Regions = dedupeTrim(r.Regions)

	fams := dedupeTrimLower(r.Families)
	for _, f := range fams {
		switch f {
		case FamilyIPv4, FamilyIPv6, FamilyUnknown:
		default:
			return GroupRule{}, fmt.Errorf("无效的 IP 家族 %q，只支持 ipv4 / ipv6 / unknown", f)
		}
	}
	out.Families = fams

	if out.MinScore < 0 || out.MinScore > ScoreMax {
		return GroupRule{}, fmt.Errorf("min_score 必须在 0-%d 之间", ScoreMax)
	}
	return out, nil
}

func dedupeTrim(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		k := strings.ToLower(v)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, v)
	}
	return out
}

func dedupeTrimLower(in []string) []string {
	out := dedupeTrim(in)
	for i := range out {
		out[i] = strings.ToLower(out[i])
	}
	return out
}

func sortGroups(gs []ProxyGroup) {
	sort.Slice(gs, func(i, j int) bool {
		if gs[i].Builtin != gs[j].Builtin {
			return gs[i].Builtin // builtins first
		}
		return gs[i].Name < gs[j].Name
	})
}

// ---- redisStore ----

func (s *redisStore) SaveGroup(ctx context.Context, g ProxyGroup) error {
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return s.rdb.HSet(ctx, keyGroups, g.Name, raw).Err()
}

func (s *redisStore) ListGroups(ctx context.Context) ([]ProxyGroup, error) {
	data, err := s.rdb.HGetAll(ctx, keyGroups).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	out := BuiltinGroups()
	for _, raw := range data {
		var g ProxyGroup
		if err := json.Unmarshal([]byte(raw), &g); err != nil {
			continue
		}
		g.Builtin = false
		out = append(out, g)
	}
	sortGroups(out)
	return out, nil
}

func (s *redisStore) GetGroup(ctx context.Context, name string) (ProxyGroup, error) {
	for _, g := range BuiltinGroups() {
		if strings.EqualFold(g.Name, name) {
			return g, nil
		}
	}
	raw, err := s.rdb.HGet(ctx, keyGroups, name).Bytes()
	if err != nil {
		return ProxyGroup{}, err
	}
	var g ProxyGroup
	if err := json.Unmarshal(raw, &g); err != nil {
		return ProxyGroup{}, err
	}
	g.Builtin = false
	return g, nil
}

func (s *redisStore) DeleteGroup(ctx context.Context, name string) error {
	return s.rdb.HDel(ctx, keyGroups, name).Err()
}

// ---- memoryStore ----

func (s *memoryStore) SaveGroup(ctx context.Context, g ProxyGroup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groups == nil {
		s.groups = map[string]ProxyGroup{}
	}
	s.groups[g.Name] = g
	return nil
}

func (s *memoryStore) ListGroups(ctx context.Context) ([]ProxyGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := BuiltinGroups()
	for _, g := range s.groups {
		g.Builtin = false
		out = append(out, g)
	}
	sortGroups(out)
	return out, nil
}

func (s *memoryStore) GetGroup(ctx context.Context, name string) (ProxyGroup, error) {
	for _, g := range BuiltinGroups() {
		if strings.EqualFold(g.Name, name) {
			return g, nil
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.groups[name]
	if !ok {
		return ProxyGroup{}, redis.Nil
	}
	g.Builtin = false
	return g, nil
}

func (s *memoryStore) DeleteGroup(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.groups, name)
	return nil
}

// ---- Service-level API ----

// ListGroups returns every group (builtin + custom) with live counts.
func (s *Service) ListGroups(ctx context.Context) ([]ProxyGroupView, error) {
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	// Hydrate a bounded window once and count locally, so N groups cost one scan.
	all, err := s.store.List(ctx, ListFilter{Page: 1, Size: 5000})
	if err != nil {
		return nil, err
	}
	out := make([]ProxyGroupView, 0, len(groups))
	for _, g := range groups {
		view := ProxyGroupView{ProxyGroup: g}
		for _, p := range all.Items {
			if !g.Rule.Matches(p) {
				continue
			}
			view.Total++
			if p.Validated {
				view.Validated++
			}
		}
		out = append(out, view)
	}
	return out, nil
}

// SaveGroup validates and persists a custom group. Builtin names are rejected.
func (s *Service) SaveGroup(ctx context.Context, name, label string, rule GroupRule) (ProxyGroup, error) {
	n, err := NormalizeGroupName(name)
	if err != nil {
		return ProxyGroup{}, err
	}
	for _, b := range BuiltinGroups() {
		if b.Name == n {
			return ProxyGroup{}, fmt.Errorf("分组名 %q 为内置分组，无法覆盖", n)
		}
	}
	nr, err := NormalizeGroupRule(rule)
	if err != nil {
		return ProxyGroup{}, err
	}
	if nr.IsEmpty() {
		return ProxyGroup{}, fmt.Errorf("分组规则不能为空，至少指定一个筛选条件")
	}
	now := time.Now().UTC()
	g := ProxyGroup{Name: n, Label: strings.TrimSpace(label), Rule: nr, UpdatedAt: now, CreatedAt: now}
	if g.Label == "" {
		g.Label = n
	}
	if existing, err := s.store.GetGroup(ctx, n); err == nil && !existing.CreatedAt.IsZero() {
		g.CreatedAt = existing.CreatedAt
	}
	if err := s.store.SaveGroup(ctx, g); err != nil {
		return ProxyGroup{}, err
	}
	s.publish("freproxies.group_saved", map[string]any{"name": n})
	return g, nil
}

// DeleteGroup removes a custom group. Builtin groups cannot be deleted.
func (s *Service) DeleteGroup(ctx context.Context, name string) error {
	n, err := NormalizeGroupName(name)
	if err != nil {
		return err
	}
	for _, b := range BuiltinGroups() {
		if b.Name == n {
			return fmt.Errorf("内置分组 %q 无法删除", n)
		}
	}
	if err := s.store.DeleteGroup(ctx, n); err != nil {
		return err
	}
	s.publish("freproxies.group_deleted", map[string]any{"name": n})
	return nil
}

// ResolveGroupFilter attaches a group's rule to a ListFilter.
func (s *Service) ResolveGroupFilter(ctx context.Context, filter ListFilter) (ListFilter, error) {
	name := strings.TrimSpace(filter.Group)
	if name == "" {
		return filter, nil
	}
	g, err := s.store.GetGroup(ctx, strings.ToLower(name))
	if err != nil {
		return filter, fmt.Errorf("分组 %q 不存在", name)
	}
	rule := g.Rule
	filter.groupRule = &rule
	return filter, nil
}
