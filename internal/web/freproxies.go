package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"unified-proxy-pool/internal/chanpolicy"
	"unified-proxy-pool/internal/directproxy"
	"unified-proxy-pool/internal/freproxies"
	"unified-proxy-pool/internal/scrapers"
	"unified-proxy-pool/internal/sourcestats"
	"unified-proxy-pool/internal/validator"
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
	if a.channels != nil {
		item.ChannelCount, item.ChannelBans = a.channels.Totals()
	}
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
		Family:   normalizeFamilyParam(firstNonEmpty(r.URL.Query().Get("family"), r.URL.Query().Get("ip_family"))),
		Group:    r.URL.Query().Get("group"),
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

// maxPickCount bounds ?count=. A caller wanting more than this wants the export
// endpoint, not a rotation batch.
const maxPickCount = 100

// pickOptionsFromRequest reads the shared selection parameters.
//
// ?channel= names the destination bucket directly; ?target= lets the caller pass
// the URL or host it is about to hit and have the pool derive the same name it
// would derive itself.
func (a *App) pickOptionsFromRequest(r *http.Request) freproxies.PickOptions {
	q := r.URL.Query()
	opt := freproxies.PickOptions{
		Protocol: firstNonEmpty(q.Get("proto"), q.Get("type"), q.Get("protocol")),
		Region:   q.Get("region"),
		Family:   normalizeFamilyParam(firstNonEmpty(q.Get("family"), q.Get("ip_family"))),
		Strategy: q.Get("strategy"),
	}
	if channel := strings.TrimSpace(q.Get("channel")); channel != "" {
		opt.Channel = chanpolicy.NormalizeChannelName(channel)
	} else if target := strings.TrimSpace(q.Get("target")); target != "" && a.channels != nil {
		opt.Channel = a.channels.ChannelFor(target)
	}
	return opt
}

// pickCount reads ?count=, returning 0 when absent so callers can keep their
// single-object response shape.
func queryTruthy(r *http.Request, keys ...string) bool {
	for _, k := range keys {
		v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(k)))
		if v == "1" || v == "true" || v == "yes" || v == "on" {
			return true
		}
	}
	return false
}

func (a *App) stickySessionKey(r *http.Request) (key string, enabled bool) {
	if queryTruthy(r, "refresh") && !queryTruthy(r, "sticky", "keep") && r.URL.Query().Get("session") == "" {
		return "", false
	}
	session := strings.TrimSpace(r.URL.Query().Get("session"))
	if session != "" {
		return "sess:" + session, true
	}
	if queryTruthy(r, "sticky", "keep", "reuse") {
		return "ip:" + clientIP(r), true
	}
	return "", false
}

func (a *App) pickWithSticky(r *http.Request, opt freproxies.PickOptions) (freproxies.Proxy, bool, error) {
	key, enabled := a.stickySessionKey(r)
	refresh := queryTruthy(r, "refresh")
	if enabled && !refresh && a.getSticky != nil {
		if addr, proto, ok := a.getSticky.GetProxy(key); ok {
			host, portStr, err := net.SplitHostPort(addr)
			if err == nil {
				port, _ := strconv.Atoi(portStr)
				return freproxies.Proxy{Host: host, Port: port, Addr: addr, Protocol: proto}, true, nil
			}
			return freproxies.Proxy{Addr: addr, Protocol: proto}, true, nil
		}
	}
	res, err := a.free.Pick(r.Context(), opt)
	if err != nil {
		return freproxies.Proxy{}, false, err
	}
	p := res.Items[0]
	if enabled && a.getSticky != nil {
		a.getSticky.PutProxy(key, p.Addr, p.Protocol)
	}
	return p, false, nil
}

func (a *App) rememberSticky(r *http.Request, p freproxies.Proxy) {
	key, enabled := a.stickySessionKey(r)
	if !enabled || a.getSticky == nil {
		return
	}
	a.getSticky.PutProxy(key, p.Addr, p.Protocol)
}

func pickCount(r *http.Request) int {
	raw := firstNonEmpty(r.URL.Query().Get("count"), r.URL.Query().Get("n"), r.URL.Query().Get("limit"))
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	if n > maxPickCount {
		n = maxPickCount
	}
	return n
}

func (a *App) handleFreeProxyRandom(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "free proxy disabled"})
		return
	}
	opt := a.pickOptionsFromRequest(r)
	count := pickCount(r)
	opt.N = count
	if opt.N <= 0 {
		opt.N = 1
	}
	if count <= 0 {
		if p, _, err := a.pickWithSticky(r, opt); err == nil {
			writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: p})
			return
		}
	}
	res, err := a.free.Pick(r.Context(), opt)
	if err != nil {
		writeError(w, err)
		return
	}
	// No ?count= keeps the historical single-object payload; asking for a batch
	// opts into the list shape.
	if count <= 0 {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: res.Items[0]})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"items":    res.Items,
		"count":    len(res.Items),
		"channel":  res.Channel,
		"strategy": res.Strategy,
		"relaxed":  res.Relaxed,
	}})
}

// normalizeFamilyParam maps user-facing aliases (4/v4/inet6/…) onto the
// canonical ipv4 / ipv6 / unknown values. Unrecognized input yields "" so the
// filter is simply ignored rather than silently matching nothing.
func normalizeFamilyParam(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "4", "v4", "ipv4", "inet", "inet4":
		return freproxies.FamilyIPv4
	case "6", "v6", "ipv6", "inet6":
		return freproxies.FamilyIPv6
	case "unknown", "host", "hostname":
		return freproxies.FamilyUnknown
	default:
		return ""
	}
}

// handlePublicGet mirrors classic proxy_pool /get — returns host:port plain or JSON.
//
// Sticky / long-lived reuse:
//
//	?sticky=1            reuse last proxy for this client IP (until TTL)
//	?session=job-42      reuse by explicit session key (cross-host)
//	(no sticky/session)  always pick a fresh proxy
//	?refresh=1           force a new pick; if sticky/session is also set, replace the cache
func (a *App) handlePublicGet(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		http.Error(w, "no proxy", http.StatusNotFound)
		return
	}
	opt := a.pickOptionsFromRequest(r)
	count := pickCount(r)
	opt.N = count
	if opt.N <= 0 {
		opt.N = 1
	}

	var res freproxies.PickResult
	var err error
	reused := false
	if count <= 0 {
		var p freproxies.Proxy
		p, reused, err = a.pickWithSticky(r, opt)
		if err == nil {
			res.Items = []freproxies.Proxy{p}
		}
	}
	if len(res.Items) == 0 {
		res, err = a.free.Pick(r.Context(), opt)
		if err != nil {
			http.Error(w, "no proxy", http.StatusNotFound)
			return
		}
		if count <= 0 {
			a.rememberSticky(r, res.Items[0])
		}
	}
	item := res.Items[0]
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "json" || r.Header.Get("Accept") == "application/json" {
		if count > 0 {
			out := make([]map[string]any, 0, len(res.Items))
			for _, it := range res.Items {
				out = append(out, publicProxyPayload(it))
			}
			writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
				"items":    out,
				"count":    len(out),
				"channel":  res.Channel,
				"strategy": res.Strategy,
				"relaxed":  res.Relaxed,
			}})
			return
		}
		payload := publicProxyPayload(item)
		payload["sticky"] = reused
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: payload})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if count > 0 {
		// One per line, matching how the plain-text endpoints are consumed
		// elsewhere in this API.
		addrs := make([]string, 0, len(res.Items))
		for _, it := range res.Items {
			addrs = append(addrs, it.Addr)
		}
		_, _ = w.Write([]byte(strings.Join(addrs, "\n")))
		return
	}
	_, _ = w.Write([]byte(item.Addr))
}

func publicProxyPayload(item freproxies.Proxy) map[string]any {
	return map[string]any{
		"proxy":     item.Addr,
		"protocol":  item.Protocol,
		"source":    item.Source,
		"score":     item.Score,
		"latency":   item.LatencyMS,
		"region":    item.Region,
		"ip_family": item.Family(),
	}
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

// handleProxySubmit adds a batch of raw addresses to the pool without
// validating them. Duplicates are silently skipped. Scripts that have
// scraped proxies locally can push them here instead of dialling Redis
// directly or running the CLI binary.
//
//	POST /api/proxies/submit
//	Authorization: Bearer <token>  OR  session cookie
//
//	Body (JSON):
//	  { "proxies": [{"host":"1.2.3.4","port":8080,"protocol":"http"},…] }
//	  or plain-text, one host:port per line (Content-Type: text/plain)
//
//	Response:
//	  { "success":true, "data":{"added":N,"submitted":M} }
func (a *App) handleProxySubmit(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "free proxy disabled"})
		return
	}

	var proxies []freproxies.Proxy
	ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.Contains(ct, "text/plain") {
		// One address per line: host:port or proto://host:port
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		for _, line := range strings.Split(string(body), "\n") {
			p := parseAddrLine(strings.TrimSpace(line))
			if p.Host != "" {
				proxies = append(proxies, p)
			}
		}
	} else {
		var body struct {
			Proxies []freproxies.Proxy `json:"proxies"`
			Source  string             `json:"source"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		proxies = body.Proxies
	}

	if len(proxies) == 0 {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "proxies list is empty"})
		return
	}

	source := strings.TrimSpace(r.URL.Query().Get("source"))
	res, err := a.free.SubmitRaw(r.Context(), proxies, source)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: submitPayload(res, len(proxies))})
}

// submitPayload shapes a SubmitResult for the wire.
//
// It reports evicted/net_growth alongside added because "added" alone is
// misleading once the raw pool is at its cap: inserting N addresses then evicts
// N others, so a caller seeing only added=N concludes the pool grew by N when it
// may not have grown at all.
func submitPayload(res freproxies.SubmitResult, submitted int) map[string]any {
	out := map[string]any{
		"submitted":  submitted,
		"parsed":     res.Parsed,
		"added":      res.Added,
		"duplicates": res.Duplicates,
		"net_growth": res.NetGrowth,
		"evicted":    res.Evicted,
		"raw_at_cap": res.RawAtCap,
	}
	if res.Evicted > 0 {
		out["note"] = fmt.Sprintf(
			"raw pool is at its cap (%d), so %d existing proxy/proxies were evicted to "+
				"admit these; pool total changed by %d",
			freproxies.MaxRawProxies, res.Evicted, res.NetGrowth)
	}
	return out
}

// handleProxyBatchTest validates up to 200 addresses concurrently and returns
// per-address results. New addresses are added to the raw pool first; live ones
// are promoted to validated; dead ones are removed, matching the periodic
// validator's behaviour.
//
//	POST /api/proxies/batch-test
//
//	Body: { "addrs": ["1.2.3.4:8080",…], "concurrency": 20, "timeout_ms": 8000 }
//
//	Response: { "success":true, "data":{"results":[{"addr":…,"ok":…,"latency_ms":…}], "ok":N,"fail":M} }
func (a *App) handleProxyBatchTest(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "free proxy disabled"})
		return
	}
	var body struct {
		Addrs       []string `json:"addrs"`
		Concurrency int      `json:"concurrency"`
		TimeoutMS   int      `json:"timeout_ms"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Addrs) == 0 {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "addrs is empty"})
		return
	}

	timeout := time.Duration(a.freeCfg.FreeValidateTimeoutMS) * time.Millisecond
	if body.TimeoutMS > 0 {
		timeout = time.Duration(body.TimeoutMS) * time.Millisecond
	}

	results := a.free.BatchTest(r.Context(), body.Addrs, a.freeCfg.FreeValidateURL, timeout, body.Concurrency)

	ok, fail := 0, 0
	for _, res := range results {
		if res.OK {
			ok++
		} else {
			fail++
		}
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"results": results,
		"ok":      ok,
		"fail":    fail,
	}})
}

// handlePublicSubmit is the unauthenticated version of handleProxySubmit for
// LAN scripts. It accepts the same body format but is rate-limited to
// plain text-only to make mass spamming harder. Auth is deliberately absent
// because scripts running alongside the panel on a private LAN often cannot
// hold a session cookie and may not have a token configured yet.
//
//	POST /api/public/submit
//
//	Body (text/plain):
//	  host:port\n…  or  proto://host:port\n…
//
//	Response: { "success":true, "data":{"added":N,"submitted":M} }
func (a *App) handlePublicSubmit(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "free proxy disabled"})
		return
	}
	if !allowPublicSubmit(publicClientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, apiResponse{Success: false, Message: "submit rate limited"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 512<<10)) // 512 KB max
	if err != nil || len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "empty body"})
		return
	}

	var proxies []freproxies.Proxy
	for _, line := range strings.Split(string(body), "\n") {
		p := parseAddrLine(strings.TrimSpace(line))
		if p.Host != "" {
			proxies = append(proxies, p)
		}
	}
	if len(proxies) == 0 {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "no valid addresses found"})
		return
	}

	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source == "" {
		source = "public-submit"
	}
	res, err := a.free.SubmitRaw(r.Context(), proxies, source)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: submitPayload(res, len(proxies))})
}

// parseAddrLine converts a single text line into a Proxy.
// Accepts: host:port, proto://host:port, socks5://user:pass@host:port.
func parseAddrLine(line string) freproxies.Proxy {
	if line == "" || strings.HasPrefix(line, "#") {
		return freproxies.Proxy{}
	}
	proto := "http"
	// Strip scheme if present.
	if idx := strings.Index(line, "://"); idx > 0 {
		proto = strings.ToLower(line[:idx])
		line = line[idx+3:]
	}
	// Strip userinfo (user:pass@)
	if at := strings.LastIndex(line, "@"); at >= 0 {
		line = line[at+1:]
	}
	host, portStr, err := splitHostPort(line)
	if err != nil || host == "" || portStr == "" {
		return freproxies.Proxy{}
	}
	port := 0
	for _, ch := range portStr {
		if ch < '0' || ch > '9' {
			return freproxies.Proxy{}
		}
		port = port*10 + int(ch-'0')
	}
	if port <= 0 || port > 65535 {
		return freproxies.Proxy{}
	}
	return freproxies.Proxy{Host: host, Port: port, Protocol: proto}
}

// splitHostPort is a thin wrapper around net.SplitHostPort so parseAddrLine
// can treat parse errors as non-fatal without sprinkling the import everywhere.
func splitHostPort(addr string) (host, port string, err error) {
	return net.SplitHostPort(addr)
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
	for i := range items {
		items[i].AutoDisabled = sourcestats.Default.IsDisabled(items[i].Name)
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
	p := validator.LiveProgress()
	b := p.Last
	item.LastBatchOK = p.OK
	item.LastBatchFail = p.Fail
	if item.LastBatchOK == 0 && item.LastBatchFail == 0 {
		item.LastBatchOK = b.OK
		item.LastBatchFail = b.Fail
	}
	item.LastBatchRaw = b.Raw
	item.LastBatchRecheck = b.Recheck
	item.LastBatchMS = b.Duration.Milliseconds()
	if !b.At.IsZero() {
		t := b.At
		item.LastBatchAt = &t
	}
	item.Running = p.Running
	item.BatchSize = p.Size
	item.BatchDone = p.OK + p.Fail
	item.LifetimeOK = p.LifetimeOK
	item.LifetimeFail = p.LifetimeFail
	item.LifetimeBatches = p.LifetimeBatches
	item.RawUnchecked = p.RawUnchecked
	if p.RawUnchecked > 0 {
		bs := p.Size
		if bs <= 0 {
			bs = 400
		}
		item.RawScanLeft = (p.RawUnchecked + bs - 1) / bs
	}
	for _, h := range p.History {
		item.History = append(item.History, freproxies.BatchHistory{
			OK: h.OK, Fail: h.Fail, Raw: h.Raw, Recheck: h.Recheck,
			DurationMS: h.Duration.Milliseconds(), At: h.At,
		})
	}
	stats := sourcestats.Default.List()
	snaps := make([]freproxies.SourceStatSnap, 0, len(stats))
	for _, st := range stats {
		snap := freproxies.SourceStatSnap{
			Name: st.Name, OK: st.OK, Fail: st.Fail,
			SuccessRate: st.SuccessRate, AvgLatencyMS: st.AvgLatencyMS,
			RecentOK: st.RecentOK, RecentFail: st.RecentFail, RecentRate: st.RecentRate,
			AutoDisabled: st.AutoDisabled,
		}
		if !st.DisabledUntil.IsZero() {
			t := st.DisabledUntil
			snap.DisabledUntil = &t
		}
		snaps = append(snaps, snap)
	}
	sort.Slice(snaps, func(i, j int) bool {
		if snaps[i].AutoDisabled != snaps[j].AutoDisabled {
			return snaps[i].AutoDisabled
		}
		if snaps[i].RecentRate != snaps[j].RecentRate {
			return snaps[i].RecentRate < snaps[j].RecentRate
		}
		return snaps[i].Fail+snaps[i].OK > snaps[j].Fail+snaps[j].OK
	})
	if len(snaps) > 30 {
		snaps = snaps[:30]
	}
	item.SourceStats = snaps
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleValidatorSourceReenable(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "name required"})
		return
	}
	sourcestats.Default.Reenable(name)
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"name": name, "reenabled": true}})
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

const maxAIProxyBodyBytes int64 = 1 << 20

// handleAIProxy accepts AI-generated proxy lists (JSON object/array or plain text)
// and submits them into the free proxy pool with an ai-* source label.
func (a *App) handleAIProxy(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "free proxy disabled"})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAIProxyBodyBytes))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiResponse{Success: false, Message: "request body exceeds 1 MB limit"})
			return
		}
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "could not read request body"})
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "empty body"})
		return
	}

	parsedInput := parseAIProxyInput(body)
	lines := parsedInput.Lines
	proxies := make([]freproxies.Proxy, 0, len(lines))
	seenAddrs := make(map[string]struct{}, len(lines))
	rejected := parsedInput.Rejected
	inputDuplicates := parsedInput.Duplicates
	for _, s := range lines {
		p := parseAddrLine(s)
		if p.Host == "" {
			rejected++
			continue
		}
		switch strings.ToLower(p.Protocol) {
		case "http", "https", "socks4", "socks5":
			p.Protocol = strings.ToLower(p.Protocol)
		case "socks", "socks5h":
			p.Protocol = "socks5"
		default:
			rejected++
			continue
		}
		// The store keys records by host:port, so protocol variants of the same
		// endpoint must be collapsed before AddRaw or Redis can over-report adds.
		addrKey := strings.ToLower(net.JoinHostPort(p.Host, strconv.Itoa(p.Port)))
		if _, exists := seenAddrs[addrKey]; exists {
			inputDuplicates++
			continue
		}
		seenAddrs[addrKey] = struct{}{}
		proxies = append(proxies, p)
	}
	if len(proxies) == 0 {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "no valid addresses found"})
		return
	}

	source := normalizeAIProxySource(r.URL.Query().Get("source"))

	res, err := a.free.SubmitRaw(r.Context(), proxies, source)
	if err != nil {
		writeError(w, err)
		return
	}
	payload := submitPayload(res, len(proxies))
	payload["source"] = source
	payload["rejected"] = rejected
	if inputDuplicates > 0 {
		payload["input_duplicates"] = inputDuplicates
		payload["duplicates"] = res.Duplicates + inputDuplicates
	}
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "ai-proxy.submit", clientIP(r), map[string]any{
			"source": source, "submitted": len(proxies), "added": res.Added,
			"rejected": rejected, "input_duplicates": inputDuplicates,
		})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: payload})
}

func normalizeAIProxySource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return "ai-unknown"
	}
	lower := strings.ToLower(source)
	if !strings.HasPrefix(lower, "ai-") && !strings.HasPrefix(lower, "ai_") {
		return "ai-" + source
	}
	return source
}

// parseAIProxyBody extracts address strings from JSON object/array or plain text.
func parseAIProxyBody(body []byte) []string {
	return parseAIProxyInput(body).Lines
}

type aiProxyInput struct {
	Lines      []string
	Rejected   int
	Duplicates int
}

func parseAIProxyInput(body []byte) aiProxyInput {
	input := strings.TrimSpace(string(body))
	if input == "" {
		return aiProxyInput{}
	}

	// {"proxies":[...]} or {"hosts":[...]} or {"items":[...]}
	if strings.HasPrefix(input, "{") {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(body, &obj); err == nil {
			foundArray := false
			rejected := 0
			for _, key := range []string{"proxies", "hosts", "items", "list", "data"} {
				raw, ok := obj[key]
				if !ok {
					continue
				}
				if lines, skipped, ok := proxyLinesFromJSONArray(raw); ok {
					foundArray = true
					rejected += skipped
					if len(lines) > 0 {
						return finalizeAIProxyInput(lines, rejected)
					}
				}
				// nested data: {"data":{"proxies":[...]}}
				var nested map[string]json.RawMessage
				if err := json.Unmarshal(raw, &nested); err == nil {
					for _, nk := range []string{"proxies", "hosts", "items", "list"} {
						if nr, ok := nested[nk]; ok {
							if lines, skipped, ok := proxyLinesFromJSONArray(nr); ok {
								foundArray = true
								rejected += skipped
								if len(lines) > 0 {
									return finalizeAIProxyInput(lines, rejected)
								}
							}
						}
					}
				}
			}
			if foundArray {
				return aiProxyInput{Rejected: rejected}
			}
		}
	}

	// ["host:port", ...]
	if strings.HasPrefix(input, "[") {
		if lines, rejected, ok := proxyLinesFromJSONArray(body); ok {
			return finalizeAIProxyInput(lines, rejected)
		}
	}

	// plain text lines
	return finalizeAIProxyInput(strings.Split(input, "\n"), 0)
}

// proxyLinesFromJSONArray accepts both strings and object records. Keeping this
// conversion in one place makes top-level and wrapped JSON behave identically.
func proxyLinesFromJSONArray(raw []byte) ([]string, int, bool) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, 0, false
	}
	out := make([]string, 0, len(items))
	rejected := 0
	for _, item := range items {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			out = append(out, s)
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(item, &m); err == nil {
			if line := proxyMapToLine(m); line != "" {
				out = append(out, line)
				continue
			}
		}
		rejected++
	}
	return out, rejected, true
}

func cleanProxyLines(in []string) []string {
	out, _ := cleanProxyLinesWithDuplicates(in)
	return out
}

func finalizeAIProxyInput(lines []string, rejected int) aiProxyInput {
	cleaned, duplicates := cleanProxyLinesWithDuplicates(lines)
	return aiProxyInput{Lines: cleaned, Rejected: rejected, Duplicates: duplicates}
}

func cleanProxyLinesWithDuplicates(in []string) ([]string, int) {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	duplicates := 0
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if _, ok := seen[s]; ok {
			duplicates++
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out, duplicates
}

func proxyMapToLine(m map[string]any) string {
	host, _ := m["host"].(string)
	if host == "" {
		host, _ = m["ip"].(string)
	}
	if host == "" {
		host, _ = m["addr"].(string)
	}
	if host == "" {
		return ""
	}
	proto, _ := m["protocol"].(string)
	if proto == "" {
		proto, _ = m["proto"].(string)
	}
	proto = strings.ToLower(strings.TrimSpace(proto))
	// already host:port or proto://host:port
	if strings.Contains(host, ":") && m["port"] == nil {
		if proto != "" && proto != "http" && !strings.Contains(host, "://") {
			return proto + "://" + host
		}
		return host
	}
	port := 0
	switch v := m["port"].(type) {
	case float64:
		if v != float64(int(v)) {
			return ""
		}
		port = int(v)
	case string:
		port, _ = strconv.Atoi(v)
	case json.Number:
		n, _ := v.Int64()
		port = int(n)
	}
	if port <= 0 || port > 65535 {
		return ""
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	if proto != "" && proto != "http" {
		return proto + "://" + addr
	}
	return addr
}

func (a *App) handleExplain(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		q = "代理池功能"
	}

	explanation := `代理池 (Unified Proxy Pool) 是融合 Super-Proxy-Pool + 多个免费代理池的 Go + React 统一代理池面板。

核心能力：
• 采集源管理（80+源，支持自定义源添加/删除）
• 出口池（一键按类型选择节点/订阅/手动节点）
• 链式代理（多跳，跳数/容错/去重/粘性等可配）
• 单跳 DirectProxy（7892）
• 渠道封禁：按目标站点临时禁用 IP（明文 HTTP 自动识别 403/429；HTTPS 需调用方 POST /api/channels/report 回传）
• 选路策略：加权随机 / 等概率 / 按渠道轮转，支持 ?count= 批量取
• AI 代理入池（POST /api/ai-proxy）
• 实时面板、性能监控、Webhook 告警

更多细节见 README.md 或 /docs/API.md。`

	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"explanation": explanation, "query": q}})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
