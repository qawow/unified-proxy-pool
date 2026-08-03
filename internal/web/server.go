package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"unified-proxy-pool/internal/apitoken"
	"unified-proxy-pool/internal/audit"
	"unified-proxy-pool/internal/auth"
	"unified-proxy-pool/internal/blacklist"
	"unified-proxy-pool/internal/config"
	"unified-proxy-pool/internal/directproxy"
	"unified-proxy-pool/internal/events"
	"unified-proxy-pool/internal/features"
	"unified-proxy-pool/internal/freproxies"
	"unified-proxy-pool/internal/mihomo"
	"unified-proxy-pool/internal/models"
	"unified-proxy-pool/internal/nodes"
	"unified-proxy-pool/internal/pools"
	"unified-proxy-pool/internal/probe"
	"unified-proxy-pool/internal/scheduler"
	"unified-proxy-pool/internal/scrapers"
	"unified-proxy-pool/internal/settings"
	"unified-proxy-pool/internal/subscriptions"
	"unified-proxy-pool/internal/traffichist"
	"unified-proxy-pool/internal/webhook"
	webassets "unified-proxy-pool/web"
)

type App struct {
	auth          *auth.Service
	settings      *settings.Service
	nodes         *nodes.Service
	subscriptions *subscriptions.Service
	pools         *pools.Service
	probe         *probe.Service
	mihomo        *mihomo.Manager
	installer     *mihomo.Installer
	events        *events.Broker
	free          *freproxies.Service
	sched         *scheduler.Scheduler
	direct        *directproxy.Server
	scrapers      *scrapers.Service
	freeCfg       config.App
	blacklist     *blacklist.Store
	audit         *audit.Store
	tokens        *apitoken.Store
	trafficHist   *traffichist.Store
	shutdown      func()
	frontend      fs.FS
	indexHTML     []byte
}

type FeatureDeps struct {
	Blacklist   *blacklist.Store
	Audit       *audit.Store
	Tokens      *apitoken.Store
	TrafficHist *traffichist.Store
}

type apiResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Message string      `json:"message,omitempty"`
}

func New(authSvc *auth.Service, settingsSvc *settings.Service, nodeSvc *nodes.Service, subSvc *subscriptions.Service, poolSvc *pools.Service, probeSvc *probe.Service, mihomoMgr *mihomo.Manager, installer *mihomo.Installer, broker *events.Broker, freeSvc *freproxies.Service, sched *scheduler.Scheduler, direct *directproxy.Server, scraperSvc *scrapers.Service, freeCfg config.App, shutdown func(), deps FeatureDeps) (*App, error) {
	frontendFS, err := fs.Sub(webassets.FS, "dist")
	if err != nil {
		return nil, fmt.Errorf("frontend dist: %w", err)
	}
	indexFile, err := frontendFS.Open("index.html")
	if err != nil {
		return nil, fmt.Errorf("frontend index.html: %w", err)
	}
	defer indexFile.Close()
	indexHTML, err := io.ReadAll(indexFile)
	if err != nil {
		return nil, fmt.Errorf("read index.html: %w", err)
	}

	return &App{
		auth:          authSvc,
		settings:      settingsSvc,
		nodes:         nodeSvc,
		subscriptions: subSvc,
		pools:         poolSvc,
		probe:         probeSvc,
		mihomo:        mihomoMgr,
		installer:     installer,
		events:        broker,
		free:          freeSvc,
		sched:         sched,
		direct:        direct,
		scrapers:      scraperSvc,
		freeCfg:       freeCfg,
		blacklist:     deps.Blacklist,
		audit:         deps.Audit,
		tokens:        deps.Tokens,
		trafficHist:   deps.TrafficHist,
		shutdown:      shutdown,
		frontend:      frontendFS,
		indexHTML:     indexHTML,
	}, nil
}

func (a *App) Router() (http.Handler, error) {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Logger)

	r.Get("/api/health", a.handleHealth)

	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/login", a.handleLogin)
		r.With(a.auth.RequireAuth).Post("/logout", a.handleLogout)
		r.With(a.auth.RequireAuth).Post("/change-password", a.handleChangePassword)
		r.With(a.auth.RequireAuth).Get("/me", a.handleMe)
	})

	r.Group(func(protected chi.Router) {
		protected.Use(a.auth.RequireAuth)

		protected.Route("/api", func(api chi.Router) {
			api.Get("/events", a.handleEvents)

			api.Get("/subscriptions", a.handleSubscriptionList)
			api.Post("/subscriptions", a.handleSubscriptionCreate)
			api.Get("/subscriptions/{id}", a.handleSubscriptionGet)
			api.Put("/subscriptions/{id}", a.handleSubscriptionUpdate)
			api.Delete("/subscriptions/{id}", a.handleSubscriptionDelete)
			api.Post("/subscriptions/{id}/toggle", a.handleSubscriptionToggle)
			api.Post("/subscriptions/{id}/sync", a.handleSubscriptionSync)
			api.Get("/subscriptions/{id}/nodes", a.handleSubscriptionNodes)
			api.Post("/subscriptions/{id}/nodes/{nodeID}/latency-test", a.handleSubscriptionNodeLatency)
			api.Post("/subscriptions/{id}/nodes/{nodeID}/speed-test", a.handleSubscriptionNodeSpeed)
			api.Post("/subscriptions/{id}/nodes/{nodeID}/toggle", a.handleSubscriptionNodeToggle)

			api.Get("/manual-nodes", a.handleManualNodeList)
			api.Post("/manual-nodes", a.handleManualNodeCreate)
			api.Get("/manual-nodes/{id}", a.handleManualNodeGet)
			api.Put("/manual-nodes/{id}", a.handleManualNodeUpdate)
			api.Delete("/manual-nodes/{id}", a.handleManualNodeDelete)
			api.Post("/manual-nodes/{id}/latency-test", a.handleManualNodeLatency)
			api.Post("/manual-nodes/{id}/speed-test", a.handleManualNodeSpeed)
			api.Post("/manual-nodes/{id}/toggle", a.handleManualNodeToggle)

			api.Get("/pools/available-candidates", a.handlePoolCandidates)
			api.Get("/pools/strategy-templates", a.handlePoolStrategyTemplates)
			api.Get("/pools", a.handlePoolList)
			api.Post("/pools", a.handlePoolCreate)
			api.Get("/pools/{id}", a.handlePoolGet)
			api.Put("/pools/{id}", a.handlePoolUpdate)
			api.Delete("/pools/{id}", a.handlePoolDelete)
			api.Post("/pools/{id}/toggle", a.handlePoolToggle)
			api.Post("/pools/{id}/publish", a.handlePoolPublish)
			api.Get("/pools/{id}/members", a.handlePoolMembers)
			api.Put("/pools/{id}/members", a.handlePoolMembersUpdate)

			api.Get("/settings", a.handleSettingsGet)
			api.Put("/settings", a.handleSettingsUpdate)
			api.Get("/mihomo/status", a.handleMihomoStatus)
			api.Get("/mihomo/release", a.handleMihomoRelease)
			api.Post("/mihomo/install", a.handleMihomoInstall)
			api.Post("/system/restart", a.handleRestart)

			api.Get("/overview", a.handleOverview)
			api.Get("/proxies", a.handleFreeProxyList)
			api.Get("/proxies/random", a.handleFreeProxyRandom)
			api.Get("/proxies/count", a.handleFreeProxyCount)
			// Prefer query/body for host:port to avoid chi path colon issues.
			api.Delete("/proxies", a.handleFreeProxyDelete)
			api.Post("/proxies/test", a.handleFreeProxyTest)
			api.Delete("/proxies/by/*", a.handleFreeProxyDelete)
			api.Post("/proxies/test/by/*", a.handleFreeProxyTest)
			api.Get("/scrapers", a.handleScraperList)
			api.Post("/scrapers", a.handleScraperCreate)
			api.Put("/scrapers/{id}", a.handleScraperUpdate)
			api.Delete("/scrapers/{id}", a.handleScraperDelete)
			api.Post("/scrapers/run-all", a.handleScraperRunAll)
			api.Post("/scrapers/{id}/toggle", a.handleScraperToggle)
			api.Post("/scrapers/{id}/run", a.handleScraperRun)
			api.Get("/stats/traffic", a.handleTrafficStats)
			api.Get("/stats/traffic/history", a.handleTrafficHistory)
			api.Get("/stats/connections", a.handleConnections)
			api.Get("/validator/queues", a.handleValidatorQueues)
			api.Post("/validator/run", a.handleValidatorRun)
			api.Get("/validator/logs", a.handleValidatorLogs)
			api.Post("/validator/logs/clear", a.handleValidatorLogsClear)
			api.Get("/proxies/export", a.handleProxyExport)
			api.Post("/proxies/purge", a.handleProxyPurge)
			api.Get("/blacklist", a.handleBlacklistList)
			api.Post("/blacklist", a.handleBlacklistAdd)
			api.Delete("/blacklist", a.handleBlacklistDelete)
			api.Get("/direct-proxy/client-pack", a.handleClientPack)
			api.Get("/scrapers/stats", a.handleScraperStats)
			api.Get("/health-board", a.handleHealthBoard)
			api.Get("/tokens", a.handleTokensList)
			api.Post("/tokens", a.handleTokenCreate)
			api.Delete("/tokens/{id}", a.handleTokenDelete)
			api.Get("/audit", a.handleAuditList)
			api.Get("/geo", a.handleGeo)
			api.Get("/direct-proxy/status", a.handleDirectProxyStatus)
			api.Put("/direct-proxy/chain", a.handleDirectProxyChainUpdate)
		})
	})

	r.Get("/metrics", a.handleMetrics)

	// Public free-proxy API (no auth) — compatible with common proxy-pool clients on LAN.
	r.Route("/api/public", func(pub chi.Router) {
		pub.Get("/proxies/random", a.handleFreeProxyRandom)
		pub.Get("/proxies/count", a.handleFreeProxyCount)
		pub.Get("/get", a.handlePublicGet) // alias: jhao104-style
		pub.Get("/count", a.handleFreeProxyCount)
		pub.Get("/health", a.handleHealth)
		pub.Post("/report", a.handlePublicReport)
	})

	fileServer := http.FileServer(http.FS(a.frontend))
	r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := strings.TrimPrefix(req.URL.Path, "/")
		if path == "" {
			a.serveIndex(w)
			return
		}
		if _, err := fs.Stat(a.frontend, path); err == nil {
			fileServer.ServeHTTP(w, req)
			return
		}
		a.serveIndex(w)
	}))

	return r, nil
}

func (a *App) serveIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(a.indexHTML)
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	token, err := a.auth.Login(r.Context(), req.Password)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Success: false, Message: "invalid password"})
		return
	}
	a.auth.SetSessionCookie(w, token)
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"authenticated": true}})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		a.auth.Logout(cookie.Value)
	}
	a.auth.ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"logged_out": true}})
}

func (a *App) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.auth.ChangePassword(r.Context(), req.OldPassword, req.NewPassword); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: err.Error()})
		return
	}
	a.auth.ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"changed": true}})
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"authenticated": true}})
}

func (a *App) handleSubscriptionList(w http.ResponseWriter, r *http.Request) {
	items, err := a.subscriptions.ListWithStats(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeList(w, items)
}

func (a *App) handleSubscriptionCreate(w http.ResponseWriter, r *http.Request) {
	var req subscriptions.UpsertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	item, err := a.subscriptions.Create(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, apiResponse{Success: true, Data: item})
}

func (a *App) handleSubscriptionGet(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	item, err := a.subscriptions.Get(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleSubscriptionUpdate(w http.ResponseWriter, r *http.Request) {
	var req subscriptions.UpsertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	item, err := a.subscriptions.Update(r.Context(), id, req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleSubscriptionDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := a.subscriptions.Delete(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	a.publishRuntimeAsync()
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"deleted": true}})
}

func (a *App) handleSubscriptionToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	item, err := a.subscriptions.Toggle(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleSubscriptionSync(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	item, err := a.subscriptions.Sync(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleSubscriptionNodes(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	items, err := a.subscriptions.ListNodes(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeList(w, items)
}

func (a *App) handleSubscriptionNodeLatency(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := requireIDParam(w, r, "nodeID")
	if !ok {
		return
	}
	if err := a.probe.EnqueueLatency("subscription", nodeID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"queued": true}})
}

func (a *App) handleSubscriptionNodeSpeed(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := requireIDParam(w, r, "nodeID")
	if !ok {
		return
	}
	if err := a.probe.EnqueueSpeed("subscription", nodeID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"queued": true}})
}

func (a *App) handleSubscriptionNodeToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	nodeID, ok := requireIDParam(w, r, "nodeID")
	if !ok {
		return
	}
	item, err := a.subscriptions.ToggleNode(r.Context(), id, nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	a.publishRuntimeAsync()
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleManualNodeList(w http.ResponseWriter, r *http.Request) {
	items, err := a.nodes.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeList(w, items)
}

func (a *App) handleManualNodeCreate(w http.ResponseWriter, r *http.Request) {
	var req nodes.CreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	items, parseErrs, err := a.nodes.Create(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, apiResponse{
		Success: true,
		Data: map[string]any{
			"items":        items,
			"parse_errors": stringifyErrors(parseErrs),
		},
	})
	a.publishRuntimeAsync()
}

func (a *App) handleManualNodeGet(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	item, err := a.nodes.Get(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleManualNodeUpdate(w http.ResponseWriter, r *http.Request) {
	var req nodes.UpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	item, err := a.nodes.Update(r.Context(), id, req)
	if err != nil {
		writeError(w, err)
		return
	}
	a.publishRuntimeAsync()
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleManualNodeDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := a.nodes.Delete(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	a.publishRuntimeAsync()
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"deleted": true}})
}

func (a *App) handleManualNodeLatency(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := a.probe.EnqueueLatency("manual", id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"queued": true}})
}

func (a *App) handleManualNodeSpeed(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := a.probe.EnqueueSpeed("manual", id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"queued": true}})
}

func (a *App) handleManualNodeToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	item, err := a.nodes.Toggle(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	a.publishRuntimeAsync()
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handlePoolCandidates(w http.ResponseWriter, r *http.Request) {
	items, err := a.pools.AvailableCandidates(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeList(w, items)
}

func (a *App) handlePoolStrategyTemplates(w http.ResponseWriter, r *http.Request) {
	writeList(w, pools.StrategyTemplates())
}

func (a *App) handlePoolList(w http.ResponseWriter, r *http.Request) {
	items, err := a.pools.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeList(w, items)
}

func (a *App) handlePoolCreate(w http.ResponseWriter, r *http.Request) {
	var req pools.UpsertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	item, err := a.pools.Create(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	a.publishRuntimeAsync()
	writeJSON(w, http.StatusCreated, apiResponse{Success: true, Data: item})
}

func (a *App) handlePoolGet(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	item, err := a.pools.Get(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handlePoolUpdate(w http.ResponseWriter, r *http.Request) {
	var req pools.UpsertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	item, err := a.pools.Update(r.Context(), id, req)
	if err != nil {
		writeError(w, err)
		return
	}
	a.publishRuntimeAsync()
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handlePoolDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := a.pools.Delete(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	a.publishRuntimeAsync()
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"deleted": true}})
}

func (a *App) handlePoolToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	item, err := a.pools.Toggle(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	a.publishRuntimeAsync()
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handlePoolPublish(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := a.pools.Publish(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"published": true}})
}

func (a *App) handlePoolMembers(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	members, err := a.pools.GetMembers(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	candidates, err := a.pools.AvailableCandidates(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"members":    members,
		"candidates": candidates,
	}})
}

func (a *App) handlePoolMembersUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Members []pools.MemberInput `json:"members"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := a.pools.UpdateMembers(r.Context(), id, req.Members); err != nil {
		writeError(w, err)
		return
	}
	a.publishRuntimeAsync()
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"updated": true}})
}

func (a *App) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	item, err := a.settings.Get(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: item})
}

func (a *App) handleMihomoStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: a.mihomo.Status()})
}

func (a *App) handleMihomoRelease(w http.ResponseWriter, r *http.Request) {
	info, err := a.installer.LatestRelease(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: info})
}

func (a *App) handleMihomoInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssetName string `json:"asset_name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	install, err := a.installer.Install(r.Context(), req.AssetName)
	if err != nil {
		writeError(w, err)
		return
	}

	currentSettings, err := a.settings.Get(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}

	activated := true
	activationError := ""
	if err := a.mihomo.ActivateBinary(r.Context(), install.InstalledPath, currentSettings.MihomoControllerSecret); err != nil {
		activated = false
		activationError = err.Error()
	}

	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"install":          install,
		"activated":        activated,
		"activation_error": activationError,
		"status":           a.mihomo.Status(),
	}})
}

func (a *App) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	var req models.Settings
	if !decodeJSON(w, r, &req) {
		return
	}
	updated, restartRequired, err := a.settings.Update(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	// hot-apply free proxy / chain runtime params
	a.freeCfg.ScrapeIntervalSec = updated.ScrapeIntervalSec
	a.freeCfg.ValidateIntervalSec = updated.ValidateIntervalSec
	a.freeCfg.FreeValidateURL = updated.FreeValidateURL
	a.freeCfg.FreeValidateTimeoutMS = updated.FreeValidateTimeoutMS
	a.freeCfg.FreeValidateConcurrency = updated.FreeValidateConcurrency
	a.freeCfg.SessionMaxAgeSec = updated.SessionMaxAgeSec
	a.freeCfg.ProxyChainHops = updated.ProxyChainHops
	if a.direct != nil && updated.ProxyChainHops >= 2 {
		a.direct.SetChainHops(updated.ProxyChainHops)
	}
	if a.auth != nil {
		a.auth.SetSessionMaxAgeSec(updated.SessionMaxAgeSec)
	}
	if a.sched != nil {
		a.sched.ApplyValidateConfig(updated.FreeValidateURL, updated.FreeValidateTimeoutMS, updated.FreeValidateConcurrency)
	}
	a.applyFeatureHot(updated.FeatureJSON)
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "settings.update", clientIP(r), map[string]any{"restart": restartRequired})
	}
	message := "已实时生效"
	if restartRequired {
		message = "已保存，重启后生效"
	} else {
		a.publishRuntimeAsync()
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"settings":      updated,
		"apply_message": message,
	}})
}

func (a *App) publishRuntimeAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = a.pools.Publish(ctx, 0)
	}()
}

func (a *App) applyFeatureHot(raw string) {
	fc := features.Parse(raw)
	webhook.Default.Configure(fc.WebhookURL, fc.WebhookEvents)
	if a.sched != nil {
		a.sched.ApplyValidateExtras(fc.ValidateURLs(a.freeCfg.FreeValidateURL), fc.SourceAutoDisableRate, fc.SourceMinSamples)
	}
	if a.direct != nil {
		a.direct.SetAuthRequired(fc.DirectAuthRequired)
		a.direct.SetAllowedCIDRs(fc.AllowedCIDRs)
		a.direct.SetRateLimit(fc.RateLimitBytesPerSec)
		// apply chain options from feature.chain
		ch := fc.Chain
		opts := a.direct.GetChainOptions()
		if ch.Hops >= 2 {
			opts.Hops = ch.Hops
		}
		if ch.FailoverTries > 0 {
			opts.FailoverTries = ch.FailoverTries
		}
		if ch.DialTimeoutMS > 0 {
			opts.DialTimeoutMS = ch.DialTimeoutMS
		}
		if ch.HopTimeoutMS > 0 {
			opts.HopTimeoutMS = ch.HopTimeoutMS
		}
		if ch.ListenAddr != "" {
			opts.ListenAddr = ch.ListenAddr
		}
		opts.Enabled = ch.Enabled || opts.Enabled
		// if chain block present with hops, trust enabled flag from parse defaults
		if ch.Hops >= 2 || ch.ListenAddr != "" || ch.FailoverTries > 0 {
			opts.Enabled = ch.Enabled
			// DefaultChain has Enabled true; if user saved enabled:false keep it
			if !ch.Enabled && ch.Hops == 0 && ch.ListenAddr == "" {
				opts.Enabled = true
			}
		}
		opts.PreferDistinctHost = ch.PreferDistinctHost
		opts.PreferDistinctRegion = ch.PreferDistinctRegion
		opts.EntryProto = ch.EntryProto
		opts.ExitProto = ch.ExitProto
		opts.EntryRegion = ch.EntryRegion
		opts.ExitRegion = ch.ExitRegion
		opts.StickyEnabled = ch.StickyEnabled
		if ch.StickyTTLSec > 0 {
			opts.StickyTTLSec = ch.StickyTTLSec
		}
		opts.AuthRequired = ch.AuthRequired
		if ch.Username != "" {
			opts.Username = ch.Username
		}
		if ch.Password != "" {
			opts.Password = ch.Password
		}
		if len(ch.AllowedCIDRs) > 0 {
			opts.AllowedCIDRs = ch.AllowedCIDRs
		}
		if ch.RateLimitBPS > 0 {
			opts.RateLimitBPS = ch.RateLimitBPS
		}
		if ch.MaxParallelDial > 0 {
			opts.MaxParallelDial = ch.MaxParallelDial
		}
		a.direct.SetChainOptions(opts)
		a.direct.SetChainHops(opts.Hops)
	}
}

// ApplyFeatureHot is used at startup.
func (a *App) ApplyFeatureHot(raw string) { a.applyFeatureHot(raw) }

func (a *App) handleRestart(w http.ResponseWriter, r *http.Request) {
	if a.shutdown != nil {
		go func() {
			time.Sleep(250 * time.Millisecond)
			a.shutdown()
		}()
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"restarting": true}})
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	id, ch := a.events.Subscribe()
	defer a.events.Unsubscribe(id)

	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-ch:
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(data)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target interface{}) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "invalid json body"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload apiResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeList always encodes list payloads as a JSON array (never null).
func writeList(w http.ResponseWriter, items interface{}) {
	raw, err := json.Marshal(items)
	if err != nil || string(raw) == "null" {
		raw = []byte("[]")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"success":true,"data":`))
	_, _ = w.Write(raw)
	_, _ = w.Write([]byte(`}`))
	_, _ = w.Write([]byte("\n"))
}

func writeError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: err.Error()})
}

func parseIDParam(r *http.Request, key string) (int64, error) {
	value := chi.URLParam(r, key)
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid %s id", key)
	}
	return id, nil
}

func requireIDParam(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	id, err := parseIDParam(r, key)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: err.Error()})
		return 0, false
	}
	return id, true
}

func stringifyErrors(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return out
}
