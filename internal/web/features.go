package web

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"unified-proxy-pool/internal/apitoken"
	"unified-proxy-pool/internal/audit"
	"unified-proxy-pool/internal/blacklist"
	"unified-proxy-pool/internal/conntrack"
	"unified-proxy-pool/internal/features"
	"unified-proxy-pool/internal/freproxies"
	"unified-proxy-pool/internal/sourcestats"
	"unified-proxy-pool/internal/traffichist"
	"unified-proxy-pool/internal/validator"
	"unified-proxy-pool/internal/webhook"
)

func (a *App) handleValidatorLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"items":   validator.DefaultLogs.List(limit),
		"running": validator.DefaultLogs.Running(),
	}})
}

func (a *App) handleValidatorLogsClear(w http.ResponseWriter, r *http.Request) {
	validator.DefaultLogs.Clear()
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "validator.logs.clear", clientIP(r), nil)
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"cleared": true}})
}

func (a *App) handleProxyExport(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		http.Error(w, "disabled", http.StatusBadRequest)
		return
	}
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "txt"
	}
	filter := freproxies.ListFilter{
		Page: 1, Size: 5000,
		Protocol: r.URL.Query().Get("proto"),
		Region:   r.URL.Query().Get("region"),
		Source:   r.URL.Query().Get("source"),
		Family:   normalizeFamilyParam(firstNonEmpty(r.URL.Query().Get("family"), r.URL.Query().Get("ip_family"))),
		Group:    r.URL.Query().Get("group"),
		Query:    r.URL.Query().Get("q"),
		OnlyOK:   r.URL.Query().Get("only_ok") == "1" || r.URL.Query().Get("only_ok") == "true",
	}
	result, err := a.free.ListProxies(r.Context(), filter)
	if err != nil {
		writeError(w, err)
		return
	}
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "proxies.export", clientIP(r), map[string]any{"count": len(result.Items), "format": format})
	}
	switch format {
	case "json":
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: result.Items})
	case "url":
		var b strings.Builder
		for _, p := range result.Items {
			scheme := p.Protocol
			if scheme == "" {
				scheme = "http"
			}
			b.WriteString(scheme)
			b.WriteString("://")
			b.WriteString(p.Addr)
			b.WriteByte('\n')
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=proxies-url.txt")
		_, _ = w.Write([]byte(b.String()))
	default:
		var b strings.Builder
		for _, p := range result.Items {
			b.WriteString(p.Addr)
			b.WriteByte('\n')
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=proxies.txt")
		_, _ = w.Write([]byte(b.String()))
	}
}

func (a *App) handleProxyPurge(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "disabled"})
		return
	}
	var body struct {
		MinScore     float64 `json:"min_score"`
		MaxFail      int     `json:"max_fail"`
		OnlyInvalid  bool    `json:"only_invalid"`
		Region       string  `json:"region"`
		Source       string  `json:"source"`
		OlderThanSec int     `json:"older_than_sec"`
		DryRun       bool    `json:"dry_run"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	list, err := a.free.ListProxies(r.Context(), freproxies.ListFilter{Page: 1, Size: 5000, Region: body.Region, Source: body.Source})
	if err != nil {
		writeError(w, err)
		return
	}
	var victims []string
	now := time.Now().UTC()
	for _, p := range list.Items {
		if body.OnlyInvalid && p.Validated {
			continue
		}
		if body.MinScore > 0 && p.Score >= body.MinScore && !body.OnlyInvalid {
			// delete below min score
			if p.Score >= body.MinScore {
				continue
			}
		}
		if body.MinScore > 0 && p.Score >= body.MinScore {
			continue
		}
		if body.MaxFail > 0 && p.FailCount < body.MaxFail {
			continue
		}
		if body.OlderThanSec > 0 && !p.LastCheck.IsZero() && now.Sub(p.LastCheck) < time.Duration(body.OlderThanSec)*time.Second {
			continue
		}
		// if no criteria except only_invalid handled, require at least one filter
		if !body.OnlyInvalid && body.MinScore <= 0 && body.MaxFail <= 0 && body.OlderThanSec <= 0 {
			continue
		}
		victims = append(victims, p.Addr)
	}
	sample := victims
	if len(sample) > 20 {
		sample = sample[:20]
	}
	deleted := 0
	if !body.DryRun {
		for _, addr := range victims {
			if err := a.free.Delete(r.Context(), addr); err == nil {
				deleted++
			}
		}
	}
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "proxies.purge", clientIP(r), map[string]any{
			"dry_run": body.DryRun, "matched": len(victims), "deleted": deleted,
		})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"matched": len(victims), "deleted": deleted, "dry_run": body.DryRun, "sample": sample,
	}})
}

func (a *App) handleBlacklistList(w http.ResponseWriter, r *http.Request) {
	if a.blacklist == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: []any{}})
		return
	}
	items, err := a.blacklist.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeList(w, items)
}

func (a *App) handleBlacklistAdd(w http.ResponseWriter, r *http.Request) {
	if a.blacklist == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "unavailable"})
		return
	}
	var body struct {
		Host   string `json:"host"`
		Addr   string `json:"addr"`
		Reason string `json:"reason"`
		TTLSec int    `json:"ttl_sec"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	host := body.Host
	if host == "" {
		host = body.Addr
	}
	item, err := a.blacklist.Add(r.Context(), host, body.Reason, body.TTLSec)
	if err != nil {
		writeError(w, err)
		return
	}
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "blacklist.add", clientIP(r), map[string]any{"host": item.Host})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleBlacklistDelete(w http.ResponseWriter, r *http.Request) {
	if a.blacklist == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "unavailable"})
		return
	}
	host := r.URL.Query().Get("host")
	if host == "" {
		host = r.URL.Query().Get("addr")
	}
	_ = a.blacklist.Remove(r.Context(), host)
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "blacklist.delete", clientIP(r), map[string]any{"host": host})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"deleted": true}})
}

func (a *App) handleClientPack(w http.ResponseWriter, r *http.Request) {
	osName := strings.ToLower(r.URL.Query().Get("os"))
	mode := strings.ToLower(r.URL.Query().Get("mode"))
	if osName == "" {
		osName = "linux"
	}
	if mode == "" {
		mode = "single"
	}
	var httpURL, socksURL string
	if a.direct != nil {
		st := a.direct.Status()
		if mode == "chain" {
			httpURL, socksURL = st.ChainHTTP, st.ChainSOCKS5
		} else {
			httpURL, socksURL = st.ClientHTTP, st.ClientSOCKS5
		}
	}
	if httpURL == "" {
		httpURL = "http://127.0.0.1:7892"
		socksURL = "socks5://127.0.0.1:7892"
	}
	var body string
	switch osName {
	case "windows":
		body = fmt.Sprintf("@echo off\r\nset http_proxy=%s\r\nset https_proxy=%s\r\nset ALL_PROXY=%s\r\necho proxy ready\r\n", httpURL, httpURL, socksURL)
		w.Header().Set("Content-Disposition", "attachment; filename=upp-proxy.bat")
	case "macos", "linux", "darwin":
		body = fmt.Sprintf("#!/usr/bin/env bash\nexport http_proxy=%s\nexport https_proxy=%s\nexport ALL_PROXY=%s\necho \"proxy ready: %s\"\ncurl -s -x %s https://httpbin.org/ip || true\n", httpURL, httpURL, socksURL, httpURL, httpURL)
		w.Header().Set("Content-Disposition", "attachment; filename=upp-proxy.sh")
	default:
		body = fmt.Sprintf("export http_proxy=%s https_proxy=%s ALL_PROXY=%s\n", httpURL, httpURL, socksURL)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

func (a *App) handleScraperStats(w http.ResponseWriter, r *http.Request) {
	writeList(w, sourcestats.Default.List())
}

func (a *App) handleHealthBoard(w http.ResponseWriter, r *http.Request) {
	board := map[string]any{}
	if a.free != nil {
		if ov, err := a.free.Overview(r.Context()); err == nil {
			board["free"] = map[string]any{
				"total": ov.TotalProxies, "validated": ov.ValidatedProxies, "raw": ov.RawProxies,
				"avg_score": ov.AvgScore, "backend": ov.Backend,
			}
		}
	}
	if a.direct != nil {
		st := a.direct.Status()
		board["direct"] = map[string]any{
			"running": st.Running, "success": st.Success, "failures": st.Failures,
			"chain_running": st.ChainRunning, "chain_success": st.ChainSuccess, "chain_failures": st.ChainFailures,
		}
	}
	board["sources"] = sourcestats.Default.List()
	board["connections"] = len(conntrack.Default.List())
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: board})
}

func (a *App) handleTokensList(w http.ResponseWriter, r *http.Request) {
	if a.tokens == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: []any{}})
		return
	}
	items, err := a.tokens.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeList(w, items)
}

func (a *App) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	if a.tokens == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "unavailable"})
		return
	}
	var body struct {
		Name   string `json:"name"`
		Scopes string `json:"scopes"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	item, err := a.tokens.Create(r.Context(), body.Name, body.Scopes)
	if err != nil {
		writeError(w, err)
		return
	}
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "token.create", clientIP(r), map[string]any{"id": item.ID, "name": item.Name})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	if a.tokens == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "unavailable"})
		return
	}
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	_ = a.tokens.Delete(r.Context(), id)
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "token.delete", clientIP(r), map[string]any{"id": id})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"deleted": true}})
}

func (a *App) handleAuditList(w http.ResponseWriter, r *http.Request) {
	if a.audit == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"items": []any{}, "total": 0}})
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	items, total, err := a.audit.List(r.Context(), page, size, r.URL.Query().Get("action"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"items": items, "total": total, "page": page, "size": size}})
}

func (a *App) handleTrafficHistory(w http.ResponseWriter, r *http.Request) {
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 {
		hours = 24
	}
	if a.trafficHist == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: []any{}})
		return
	}
	items, err := a.trafficHist.History(r.Context(), hours)
	if err != nil {
		writeError(w, err)
		return
	}
	writeList(w, items)
}

func (a *App) handleConnections(w http.ResponseWriter, r *http.Request) {
	writeList(w, conntrack.Default.List())
}

func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("# HELP upp_info Unified Proxy Pool metrics\n")
	if a.free != nil {
		if total, validated, raw, err := a.free.Store().Count(r.Context()); err == nil {
			fmt.Fprintf(&b, "upp_proxies_total %d\n", total)
			fmt.Fprintf(&b, "upp_proxies_validated %d\n", validated)
			fmt.Fprintf(&b, "upp_proxies_raw %d\n", raw)
		}
	}
	snap := map[string]int64{}
	// traffic via overview path
	if a.free != nil {
		// use traffic package indirectly through freproxies overview traffic attach — pull Default
	}
	fmt.Fprintf(&b, "upp_active_connections %d\n", len(conntrack.Default.List()))
	if a.direct != nil {
		st := a.direct.Status()
		fmt.Fprintf(&b, "upp_direct_success %d\n", st.Success)
		fmt.Fprintf(&b, "upp_direct_failures %d\n", st.Failures)
		fmt.Fprintf(&b, "upp_chain_success %d\n", st.ChainSuccess)
		fmt.Fprintf(&b, "upp_chain_failures %d\n", st.ChainFailures)
	}
	_ = snap
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(b.String()))
}

func (a *App) handlePublicReport(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "disabled"})
		return
	}
	var body struct {
		Addr      string `json:"addr"`
		OK        bool   `json:"ok"`
		LatencyMS int64  `json:"latency_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Addr == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "addr required"})
		return
	}
	_ = a.free.Store().MarkValidated(r.Context(), body.Addr, body.LatencyMS, body.OK)
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"accepted": true}})
}

func (a *App) enhancedHealth(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"ok": true, "service": "unified-proxy-pool"}
	if a.free != nil {
		if total, validated, raw, err := a.free.Store().Count(r.Context()); err == nil {
			data["total"] = total
			data["validated"] = validated
			data["raw"] = raw
			data["backend"] = a.free.Store().Backend()
			fc := a.settings.FeatureConfig(r.Context())
			if fc.AlertValidatedMin > 0 && int(validated) < fc.AlertValidatedMin {
				webhook.Default.Notify("validated_low", map[string]any{"validated": validated, "min": fc.AlertValidatedMin})
			}
		}
	}
	if a.direct != nil {
		st := a.direct.Status()
		data["direct_proxy"] = st.Running
		data["chain_proxy"] = st.ChainRunning
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: data})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Real-IP"); xff != "" {
		return xff
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ensure types referenced
var (
	_ = blacklist.Entry{}
	_ = audit.Entry{}
	_ = apitoken.Token{}
	_ = traffichist.Sample{}
	_ = features.Config{}
)
