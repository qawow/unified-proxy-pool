package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"unified-proxy-pool/internal/directproxy"
	"unified-proxy-pool/internal/freproxies"
	"unified-proxy-pool/internal/scrapers"
	"unified-proxy-pool/internal/traffic"
)

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	a.enhancedHealth(w, r)
}

func (a *App) handleOverview(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"enabled": false, "traffic": traffic.Get(r.Context())}})
		return
	}
	item, err := a.free.Overview(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	item.Traffic = traffic.Get(r.Context())
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleTrafficStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: traffic.Get(r.Context())})
}

func (a *App) handleFreeProxyList(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: freproxies.ListResult{Items: []freproxies.Proxy{}}})
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	filter := freproxies.ListFilter{
		Page:     page,
		Size:     size,
		Source:   r.URL.Query().Get("source"),
		Protocol: firstNonEmpty(r.URL.Query().Get("proto"), r.URL.Query().Get("protocol")),
		Region:   r.URL.Query().Get("region"),
		Query:    firstNonEmpty(r.URL.Query().Get("q"), r.URL.Query().Get("query")),
		OnlyOK:   r.URL.Query().Get("only_ok") == "1" || r.URL.Query().Get("only_ok") == "true",
	}
	if ms := r.URL.Query().Get("min_score"); ms != "" {
		if v, err := strconv.ParseFloat(ms, 64); err == nil {
			filter.MinScore = v
		}
	}
	result, err := a.free.ListProxies(r.Context(), filter)
	if err != nil {
		writeError(w, err)
		return
	}
	if result.Items == nil {
		result.Items = []freproxies.Proxy{}
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: result})
}

func (a *App) handleFreeProxyRandom(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "free proxy disabled"})
		return
	}
	proto := firstNonEmpty(r.URL.Query().Get("proto"), r.URL.Query().Get("type"), r.URL.Query().Get("protocol"))
	region := r.URL.Query().Get("region")
	item, err := a.free.RandomFilter(r.Context(), proto, region)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

// handlePublicGet mirrors classic proxy_pool /get — returns host:port plain or JSON.
func (a *App) handlePublicGet(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		http.Error(w, "no proxy", http.StatusNotFound)
		return
	}
	proto := r.URL.Query().Get("type")
	if proto == "" {
		proto = r.URL.Query().Get("proto")
	}
	region := r.URL.Query().Get("region")
	item, err := a.free.RandomFilter(r.Context(), proto, region)
	if err != nil {
		http.Error(w, "no proxy", http.StatusNotFound)
		return
	}
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "json" || r.Header.Get("Accept") == "application/json" {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
			"proxy":    item.Addr,
			"protocol": item.Protocol,
			"source":   item.Source,
			"score":    item.Score,
			"latency":  item.LatencyMS,
			"region":   item.Region,
		}})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(item.Addr))
}

func (a *App) handleFreeProxyCount(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]int64{"total": 0, "validated": 0, "raw": 0}})
		return
	}
	total, validated, raw, err := a.free.Store().Count(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]int64{
		"total": total, "validated": validated, "raw": raw, "count": validated,
	}})
}

func proxyAddrFromRequest(r *http.Request) string {
	if q := strings.TrimSpace(r.URL.Query().Get("proxy")); q != "" {
		return q
	}
	if q := strings.TrimSpace(r.URL.Query().Get("addr")); q != "" {
		return q
	}
	if v := strings.TrimSpace(chi.URLParam(r, "addr")); v != "" {
		return v
	}
	if v := strings.TrimSpace(chi.URLParam(r, "*")); v != "" {
		return strings.TrimPrefix(v, "/")
	}
	return ""
}

func (a *App) handleFreeProxyDelete(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "free proxy disabled"})
		return
	}
	addr := proxyAddrFromRequest(r)
	if addr == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "proxy/addr required"})
		return
	}
	if err := a.free.Delete(r.Context(), addr); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"deleted": true, "addr": addr}})
}

func (a *App) handleFreeProxyTest(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "free proxy disabled"})
		return
	}
	addr := proxyAddrFromRequest(r)
	if addr == "" && r.Body != nil {
		var body struct {
			Addr  string `json:"addr"`
			Proxy string `json:"proxy"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Addr != "" {
			addr = body.Addr
		} else if body.Proxy != "" {
			addr = body.Proxy
		}
	}
	if addr == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "proxy/addr required"})
		return
	}
	item, err := a.free.TestProxy(r.Context(), addr, a.freeCfg.FreeValidateURL, time.Duration(a.freeCfg.FreeValidateTimeoutMS)*time.Millisecond)
	if err != nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"ok": false, "proxy": item, "error": err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"ok": true, "proxy": item}})
}

func (a *App) handleScraperList(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeList(w, []any{})
		return
	}
	items, err := a.free.ListScrapers(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeList(w, items)
}

func (a *App) handleScraperCreate(w http.ResponseWriter, r *http.Request) {
	if a.scrapers == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "scrapers unavailable"})
		return
	}
	var req scrapers.UpsertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	item, err := a.scrapers.Create(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleScraperUpdate(w http.ResponseWriter, r *http.Request) {
	if a.scrapers == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "scrapers unavailable"})
		return
	}
	var req scrapers.UpsertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	item, err := a.scrapers.Update(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleScraperDelete(w http.ResponseWriter, r *http.Request) {
	if a.scrapers == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "scrapers unavailable"})
		return
	}
	if err := a.scrapers.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"deleted": true}})
}

func (a *App) handleScraperToggle(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "free proxy disabled"})
		return
	}
	item, err := a.free.ToggleScraper(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleScraperRun(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "free proxy disabled"})
		return
	}
	name := chi.URLParam(r, "id")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if _, err := a.free.RunScraper(ctx, name); err != nil {
			// event already recorded inside service on failure paths
			_ = err
		}
	}()
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"started": true, "name": name}})
}

func (a *App) handleScraperRunAll(w http.ResponseWriter, r *http.Request) {
	if a.sched == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "scheduler disabled"})
		return
	}
	a.sched.TriggerScrape(context.Background())
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"started": true}})
}

func (a *App) handleValidatorQueues(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: freproxies.ValidatorQueues{}})
		return
	}
	item, err := a.free.Queues(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleValidatorRun(w http.ResponseWriter, r *http.Request) {
	if a.sched == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "scheduler disabled"})
		return
	}
	a.sched.TriggerValidate(context.Background())
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"started": true}})
}

func (a *App) handleGeo(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: []freproxies.RegionCount{}})
		return
	}
	items, err := a.free.Store().RegionTop(r.Context(), 20)
	if err != nil {
		writeError(w, err)
		return
	}
	writeList(w, items)
}

func (a *App) handleDirectProxyStatus(w http.ResponseWriter, r *http.Request) {
	if a.direct == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"enabled": false, "running": false}})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: a.direct.Status()})
}

func (a *App) handleDirectProxyChainUpdate(w http.ResponseWriter, r *http.Request) {
	if a.direct == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "direct proxy disabled"})
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "invalid json body"})
		return
	}
	cur := a.direct.GetChainOptions()
	var body directproxy.ChainOptions
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "invalid json body"})
		return
	}
	// Detect full options vs hops-only
	var probe map[string]json.RawMessage
	_ = json.Unmarshal(raw, &probe)
	if hopsRaw, ok := probe["hops"]; ok {
		var h int
		_ = json.Unmarshal(hopsRaw, &h)
		if h >= 2 {
			cur.Hops = h
		}
	}
	if _, ok := probe["failover_tries"]; ok && body.FailoverTries > 0 {
		cur.FailoverTries = body.FailoverTries
	}
	if _, ok := probe["dial_timeout_ms"]; ok && body.DialTimeoutMS > 0 {
		cur.DialTimeoutMS = body.DialTimeoutMS
	}
	if _, ok := probe["hop_timeout_ms"]; ok && body.HopTimeoutMS > 0 {
		cur.HopTimeoutMS = body.HopTimeoutMS
	}
	if _, ok := probe["listen_addr"]; ok && body.ListenAddr != "" {
		cur.ListenAddr = body.ListenAddr
	}
	if _, ok := probe["enabled"]; ok {
		cur.Enabled = body.Enabled
	}
	if _, ok := probe["prefer_distinct_host"]; ok {
		cur.PreferDistinctHost = body.PreferDistinctHost
	}
	if _, ok := probe["prefer_distinct_region"]; ok {
		cur.PreferDistinctRegion = body.PreferDistinctRegion
	}
	if _, ok := probe["entry_proto"]; ok {
		cur.EntryProto = body.EntryProto
	}
	if _, ok := probe["exit_proto"]; ok {
		cur.ExitProto = body.ExitProto
	}
	if _, ok := probe["entry_region"]; ok {
		cur.EntryRegion = body.EntryRegion
	}
	if _, ok := probe["exit_region"]; ok {
		cur.ExitRegion = body.ExitRegion
	}
	if _, ok := probe["sticky_enabled"]; ok {
		cur.StickyEnabled = body.StickyEnabled
	}
	if body.StickyTTLSec > 0 {
		cur.StickyTTLSec = body.StickyTTLSec
	}
	if _, ok := probe["auth_required"]; ok {
		cur.AuthRequired = body.AuthRequired
	}
	if body.Username != "" {
		cur.Username = body.Username
	}
	if body.Password != "" {
		cur.Password = body.Password
	}
	if _, ok := probe["allowed_cidrs"]; ok {
		cur.AllowedCIDRs = body.AllowedCIDRs
	}
	if body.RateLimitBPS > 0 {
		cur.RateLimitBPS = body.RateLimitBPS
	}
	if body.MaxParallelDial > 0 {
		cur.MaxParallelDial = body.MaxParallelDial
	}

	a.direct.SetChainOptions(cur)
	a.direct.SetChainHops(cur.Hops)

	// persist into settings feature.chain
	if a.settings != nil {
		if st, err := a.settings.Get(r.Context()); err == nil {
			st.ProxyChainHops = cur.Hops
			feat := st.Feature
			if feat == nil {
				feat = map[string]any{}
			}
			b, _ := json.Marshal(cur)
			var chainMap map[string]any
			_ = json.Unmarshal(b, &chainMap)
			feat["chain"] = chainMap
			st.Feature = feat
			rawFeat, _ := json.Marshal(feat)
			st.FeatureJSON = string(rawFeat)
			_, _, _ = a.settings.Update(r.Context(), st)
		}
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: a.direct.Status()})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
