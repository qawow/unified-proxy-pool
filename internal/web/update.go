package web

import (
	"context"
	"log"
	"net/http"
	"time"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/update"
	"unified-proxy-pool/internal/version"
)

// updateFallbackClient dials GitHub through the pool when the direct link is
// blocked. Certificate verification stays ON (unlike the scraper client): the
// exit node must not be able to see or rewrite what we are about to exec.
func (a *App) updateFallbackClient() *http.Client {
	u := a.autoMihomoScrapeProxy()
	if u == "" {
		return nil
	}
	return crawlers.NewVerifiedHTTPClientWithProxy(5*time.Minute, u).Unwrap()
}

func (a *App) updater() *update.Service {
	return a.updateSvc
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	svc := a.updater()
	st, err := svc.Prepare(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiResponse{Success: false, Message: err.Error(), Data: st})
		return
	}
	// The binary is installed; exec would replace this process image and the
	// client would never see a reply, so answer first and restart afterwards.
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: st, Message: "updated, restarting into new binary"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		// Give the response a moment to leave the socket, then tear down the
		// children we own — exec does not, so mihomo would otherwise survive as
		// an orphan holding the controller and listener ports.
		time.Sleep(500 * time.Millisecond)
		if a.mihomo != nil {
			a.mihomo.Stop()
		}
		if err := svc.Restart(); err != nil {
			log.Printf("hot update: exec new binary failed: %v", err)
		}
	}()
}
