package web

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"unified-proxy-pool/internal/freproxies"
)

type groupPayload struct {
	Name      string   `json:"name"`
	Label     string   `json:"label"`
	Sources   []string `json:"sources"`
	Protocols []string `json:"protocols"`
	Families  []string `json:"families"`
	Regions   []string `json:"regions"`
	MinScore  float64  `json:"min_score"`
	OnlyOK    bool     `json:"only_ok"`
}

func (p groupPayload) rule() freproxies.GroupRule {
	return freproxies.GroupRule{
		Sources:   p.Sources,
		Protocols: p.Protocols,
		Families:  p.Families,
		Regions:   p.Regions,
		MinScore:  p.MinScore,
		OnlyOK:    p.OnlyOK,
	}
}

// GET /api/proxies/groups — builtin + custom groups with live counts.
func (a *App) handleProxyGroupList(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: []freproxies.ProxyGroupView{}})
		return
	}
	items, err := a.free.ListGroups(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if items == nil {
		items = []freproxies.ProxyGroupView{}
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: items})
}

// POST /api/proxies/groups — create or update a custom group.
func (a *App) handleProxyGroupSave(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "free proxy disabled"})
		return
	}
	var body groupPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	g, err := a.free.SaveGroup(r.Context(), body.Name, body.Label, body.rule())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: err.Error()})
		return
	}
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "proxy_group.save", clientIP(r), map[string]any{"name": g.Name})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: g})
}

// PUT /api/proxies/groups/{name} — update an existing custom group.
func (a *App) handleProxyGroupUpdate(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "free proxy disabled"})
		return
	}
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	var body groupPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	// Path wins over body so the URL is always authoritative.
	g, err := a.free.SaveGroup(r.Context(), name, body.Label, body.rule())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: err.Error()})
		return
	}
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "proxy_group.update", clientIP(r), map[string]any{"name": g.Name})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: g})
}

// DELETE /api/proxies/groups/{name} — remove a custom group.
func (a *App) handleProxyGroupDelete(w http.ResponseWriter, r *http.Request) {
	if a.free == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "free proxy disabled"})
		return
	}
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if err := a.free.DeleteGroup(r.Context(), name); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: err.Error()})
		return
	}
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "proxy_group.delete", clientIP(r), map[string]any{"name": name})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]string{"deleted": name}})
}
