package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"unified-proxy-pool/internal/chanpolicy"
)

// maxReportItems caps one batch report. Reports arrive from callers on the hot
// path of their own scrapers, so the bound protects the panel from a runaway
// client rather than from abuse.
const maxReportItems = 500

// maxReportBody matches the AI ingest limit so the two script-facing endpoints
// behave the same way.
const maxReportBody = 1 << 20 // 1 MiB

// publicReportLimit is per source IP per second on the unauthenticated
// report path. Authenticated /api/channels/report is not limited here.
const publicReportLimit = 50

type ipWindow struct {
	second int64
	count  int
}

var publicReportLimiter = struct {
	mu sync.Mutex
	m  map[string]ipWindow
}{m: map[string]ipWindow{}}

func allowPublicReport(ip string) bool {
	if ip == "" {
		ip = "unknown"
	}
	now := time.Now().Unix()
	publicReportLimiter.mu.Lock()
	defer publicReportLimiter.mu.Unlock()
	w := publicReportLimiter.m[ip]
	if w.second != now {
		w = ipWindow{second: now, count: 0}
	}
	w.count++
	publicReportLimiter.m[ip] = w
	// Drop stale windows so the map cannot grow without bound.
	if len(publicReportLimiter.m) > 4096 {
		for k, v := range publicReportLimiter.m {
			if v.second < now-2 {
				delete(publicReportLimiter.m, k)
			}
		}
	}
	return w.count <= publicReportLimit
}

type channelReportItem struct {
	Channel   string `json:"channel"`
	Target    string `json:"target"`
	Addr      string `json:"addr"`
	Proxy     string `json:"proxy"`
	OK        *bool  `json:"ok"`
	Status    int    `json:"status"`
	Err       string `json:"err"`
	LatencyMS int64  `json:"latency_ms"`
}

type channelReportRequest struct {
	channelReportItem
	Items []channelReportItem `json:"items"`
}

// channelOf resolves the channel for one reported item, accepting either an
// explicit channel name or the target it was aimed at.
func (a *App) channelOf(it channelReportItem) string {
	if name := strings.TrimSpace(it.Channel); name != "" {
		return chanpolicy.NormalizeChannelName(name)
	}
	if target := strings.TrimSpace(it.Target); target != "" && a.channels != nil {
		return a.channels.ChannelFor(target)
	}
	return ""
}

func (it channelReportItem) addr() string {
	if v := strings.TrimSpace(it.Addr); v != "" {
		return v
	}
	return strings.TrimSpace(it.Proxy)
}

// ok defaults to false when omitted: a report with no verdict is only worth
// sending because something went wrong, and defaulting to success would let a
// malformed client silently clear a proxy's record.
func (it channelReportItem) ok() bool {
	if it.OK != nil {
		return *it.OK
	}
	return it.Status > 0 && it.Status < 400
}

// POST /api/channels/report — caller-reported outcomes.
//
// This is the only way the pool learns application-layer verdicts for HTTPS: the
// CONNECT tunnel is opaque, so a 403 or a captcha is invisible to the proxy and
// has to come back from whoever read the response.
func (a *App) handleChannelReport(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "channel policy disabled"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/public/") && !allowPublicReport(publicClientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, apiResponse{Success: false, Message: "too many reports"})
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxReportBody+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "read body failed"})
		return
	}
	if len(body) > maxReportBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, apiResponse{Success: false, Message: "body too large (max 1MB)"})
		return
	}

	var req channelReportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "invalid json body"})
		return
	}
	items := req.Items
	if len(items) == 0 {
		items = []channelReportItem{req.channelReportItem}
	}
	if len(items) > maxReportItems {
		writeJSON(w, http.StatusBadRequest, apiResponse{
			Success: false,
			Message: "too many items (max " + strconv.Itoa(maxReportItems) + ")",
		})
		return
	}

	accepted, rejected := 0, 0
	bans := make([]chanpolicy.Ban, 0, 4)
	for _, it := range items {
		channel, addr := a.channelOf(it), it.addr()
		if channel == "" || addr == "" {
			rejected++
			continue
		}
		accepted++
		errTag := strings.TrimSpace(it.Err)
		if errTag == "" && !it.ok() {
			errTag = "reported"
		}
		if b := a.channels.Record(chanpolicy.Outcome{
			Channel:   channel,
			Addr:      addr,
			OK:        it.ok(),
			Status:    it.Status,
			Err:       errTag,
			LatencyMS: it.LatencyMS,
			Reported:  true,
		}); b != nil {
			bans = append(bans, *b)
		}
		// A reported failure is also evidence about the proxy itself, so keep the
		// global score in step with the classic /api/public/report behaviour.
		if a.free != nil {
			_ = a.free.Store().MarkValidated(r.Context(), addr, it.LatencyMS, it.ok())
		}
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"accepted": accepted,
		"rejected": rejected,
		"banned":   bans,
	}})
}

// GET /api/channels — per-channel summary, worst first.
func (a *App) handleChannelList(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeList(w, []chanpolicy.ChannelStat{})
		return
	}
	items := a.channels.Channels()
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))); q != "" {
		filtered := make([]chanpolicy.ChannelStat, 0, len(items))
		for _, it := range items {
			if strings.Contains(strings.ToLower(it.Name), q) {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	if only := r.URL.Query().Get("only_banned"); only == "1" || only == "true" {
		filtered := make([]chanpolicy.ChannelStat, 0, len(items))
		for _, it := range items {
			if it.Bans > 0 {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	writeList(w, items)
}

// GET /api/channels/{name}/bans
func (a *App) handleChannelBans(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeList(w, []chanpolicy.Ban{})
		return
	}
	name := channelNameParam(r)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "channel required"})
		return
	}
	writeList(w, a.channels.Bans(name))
}

// DELETE /api/channels/{name}/bans?addr=1.2.3.4:8080
func (a *App) handleChannelUnban(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "channel policy disabled"})
		return
	}
	name := channelNameParam(r)
	addr := strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("addr"), r.URL.Query().Get("proxy")))
	if name == "" || addr == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "channel and addr required"})
		return
	}
	released := a.channels.Unban(name, addr)
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "channel.unban", clientIP(r), map[string]any{
			"channel": name, "addr": addr, "released": released,
		})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"channel": name, "addr": addr, "released": released,
	}})
}

// POST /api/channels/{name}/reset — clear bans and counters, keep the channel.
func (a *App) handleChannelReset(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "channel policy disabled"})
		return
	}
	name := channelNameParam(r)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "channel required"})
		return
	}
	reset := a.channels.ResetChannel(name)
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "channel.reset", clientIP(r), map[string]any{"channel": name})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"channel": name, "reset": reset,
	}})
}

// DELETE /api/channels/{name} — forget the channel entirely.
func (a *App) handleChannelDelete(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "channel policy disabled"})
		return
	}
	name := channelNameParam(r)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "channel required"})
		return
	}
	deleted := a.channels.DeleteChannel(name)
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "channel.delete", clientIP(r), map[string]any{"channel": name})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"channel": name, "deleted": deleted,
	}})
}

// channelNameParam reads the channel from the path, falling back to the query so
// the endpoints stay usable for names chi would rather not route.
func channelNameParam(r *http.Request) string {
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if name == "" {
		name = strings.TrimSpace(r.URL.Query().Get("channel"))
	}
	return chanpolicy.NormalizeChannelName(name)
}

// GET /api/channels/logs?channel=&limit=
func (a *App) handleChannelLogs(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
			"items": []chanpolicy.LogEntry{},
		}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 150
	}
	if limit > 500 {
		limit = 500
	}
	channel := strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("channel"), chi.URLParam(r, "name")))
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"items": a.channels.Logs(channel, limit),
	}})
}

// POST /api/channels/logs/clear
func (a *App) handleChannelLogsClear(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "channel policy disabled"})
		return
	}
	a.channels.ClearLogs()
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"cleared": true}})
}

func (a *App) handleAllowList(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeList(w, []chanpolicy.Allow{})
		return
	}
	writeList(w, a.channels.Allows())
}

func (a *App) handleAllowAdd(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "channel policy disabled"})
		return
	}
	var body struct {
		Channel string `json:"channel"`
		Addr    string `json:"addr"`
		Proxy   string `json:"proxy"`
		Reason  string `json:"reason"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	addr := strings.TrimSpace(firstNonEmpty(body.Addr, body.Proxy))
	if addr == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "addr required"})
		return
	}
	a.channels.Allow(body.Channel, addr, body.Reason)
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "channel.allow", clientIP(r), map[string]any{
			"channel": body.Channel, "addr": addr,
		})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]string{
		"channel": chanpolicy.NormalizeChannelName(body.Channel), "addr": addr,
	}})
}

func (a *App) handleAllowDelete(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "channel policy disabled"})
		return
	}
	channel := r.URL.Query().Get("channel")
	addr := firstNonEmpty(r.URL.Query().Get("addr"), r.URL.Query().Get("proxy"))
	if strings.TrimSpace(addr) == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "addr required"})
		return
	}
	removed := a.channels.Deny(channel, addr)
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "channel.deny", clientIP(r), map[string]any{
			"channel": channel, "addr": addr, "removed": removed,
		})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"channel": channel, "addr": addr, "removed": removed,
	}})
}

func (a *App) handleRuleList(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeList(w, []chanpolicy.Rule{})
		return
	}
	writeList(w, a.channels.Rules())
}

func (a *App) handleRuleAdd(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "channel policy disabled"})
		return
	}
	var body chanpolicy.Rule
	if !decodeJSON(w, r, &body) {
		return
	}
	rule, err := a.channels.AddRule(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: err.Error()})
		return
	}
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "channel.rule.add", clientIP(r), map[string]any{
			"id": rule.ID, "kind": rule.Kind, "channel": rule.Channel,
		})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: rule})
}

func (a *App) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	if a.channels == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "channel policy disabled"})
		return
	}
	id := strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("id"), chi.URLParam(r, "id")))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "id required"})
		return
	}
	ok := a.channels.DeleteRule(id)
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "channel.rule.delete", clientIP(r), map[string]any{"id": id})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"id": id, "deleted": ok}})
}
