package web

import (
	"context"
	"net/http"
	"time"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/update"
	"unified-proxy-pool/internal/version"
)

func (a *App) updater() *update.Service {
	primary := &http.Client{Timeout: 90 * time.Second}
	var fallback *http.Client
	if u := a.autoMihomoScrapeProxy(); u != "" {
		fallback = crawlers.NewHTTPClientWithProxy(90*time.Second, u).Unwrap()
	}
	return update.New("", "", primary, fallback)
}

func (a *App) handleSystemVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: version.Info()})
}

func (a *App) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	st, err := a.updater().Check(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiResponse{Success: false, Message: err.Error(), Data: st})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: st})
}

func (a *App) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	st, err := a.updater().Apply(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiResponse{Success: false, Message: err.Error(), Data: st})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: st, Message: "executing new binary"})
}
