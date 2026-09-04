package web

import (
	"net/http"
	"strconv"

	"unified-proxy-pool/internal/cfscan"
)

func (a *App) handleCFScanStatus(w http.ResponseWriter, r *http.Request) {
	if a.cfscan == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: cfscan.Status{}})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: a.cfscan.Status()})
}

func (a *App) handleCFScanHits(w http.ResponseWriter, r *http.Request) {
	if a.cfscan == nil {
		writeList(w, []any{})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := a.cfscan.ListHits(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeList(w, hits)
}

func (a *App) handleCFScanRun(w http.ResponseWriter, r *http.Request) {
	if a.cfscan == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "cfscan disabled"})
		return
	}
	var req cfscan.RunRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.cfscan.Start(req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: a.cfscan.Status()})
}

func (a *App) handleCFScanStop(w http.ResponseWriter, r *http.Request) {
	if a.cfscan != nil {
		a.cfscan.Stop()
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true})
}

func (a *App) handleCFScanClear(w http.ResponseWriter, r *http.Request) {
	if a.cfscan == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true})
		return
	}
	if err := a.cfscan.ClearHits(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true})
}

func (a *App) handleCFScanApply(w http.ResponseWriter, r *http.Request) {
	if a.cfscan == nil || a.nodes == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "unavailable"})
		return
	}
	var body struct {
		NodeID int64    `json:"node_id"`
		IPs    []string `json:"ips"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ips := body.IPs
	if len(ips) == 0 {
		hits, err := a.cfscan.ListHits(r.Context(), 200)
		if err != nil {
			writeError(w, err)
			return
		}
		for _, h := range hits {
			ips = append(ips, h.IP)
		}
	}
	n, err := a.nodes.CloneWithServers(r.Context(), body.NodeID, ips)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"created": n}})
}

func (a *App) handleCFScanExport(w http.ResponseWriter, r *http.Request) {
	if a.cfscan == nil {
		http.Error(w, "disabled", http.StatusBadRequest)
		return
	}
	hits, err := a.cfscan.ListHits(r.Context(), 5000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=cf_proxy_ips.txt")
	for _, h := range hits {
		_, _ = w.Write([]byte(h.IP + "\n"))
	}
}
