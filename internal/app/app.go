package app

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"unified-proxy-pool/internal/apitoken"
	"unified-proxy-pool/internal/audit"
	"unified-proxy-pool/internal/auth"
	"unified-proxy-pool/internal/blacklist"
	"unified-proxy-pool/internal/chanpolicy"
	"unified-proxy-pool/internal/config"
	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/directproxy"
	"unified-proxy-pool/internal/events"
	"unified-proxy-pool/internal/features"
	"unified-proxy-pool/internal/freproxies"
	"unified-proxy-pool/internal/geoip"
	"unified-proxy-pool/internal/mihomo"
	"unified-proxy-pool/internal/nodes"
	"unified-proxy-pool/internal/pools"
	"unified-proxy-pool/internal/probe"
	"unified-proxy-pool/internal/proxy"
	"unified-proxy-pool/internal/scheduler"
	"unified-proxy-pool/internal/scrapers"
	"unified-proxy-pool/internal/settings"
	"unified-proxy-pool/internal/sourcestats"
	"unified-proxy-pool/internal/sticky"
	"unified-proxy-pool/internal/subscriptions"
	"unified-proxy-pool/internal/traffichist"
	"unified-proxy-pool/internal/validator"
	"unified-proxy-pool/internal/web"
	"unified-proxy-pool/internal/webhook"
)

func Run() {
	cfg := config.Load()
	if err := config.EnsureDirs(cfg); err != nil {
		log.Fatalf("ensure dirs: %v", err)
	}

	store, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer store.Close()

	settingsSvc := settings.NewService(store, cfg)
	defaultHash, err := auth.HashPassword("admin")
	if err != nil {
		log.Fatalf("hash default password: %v", err)
	}
	if err := settingsSvc.EnsureDefaults(context.Background(), defaultHash); err != nil {
		log.Fatalf("ensure default settings: %v", err)
	}

	broker := events.NewBroker()
	authSvc := auth.NewService(settingsSvc, store, cfg.SessionMaxAgeSec)
	nodeSvc := nodes.NewService(store, broker)
	subSvc := subscriptions.NewService(store, settingsSvc, broker)
	subSvc.SetLocalExits(cfg.DirectProxyAddr, cfg.ProxyChainAddr)
	rootCtx, rootCancel := context.WithCancel(context.Background())
	var shutdownOnce sync.Once
	requestShutdown := func() {
		shutdownOnce.Do(rootCancel)
	}

	currentSettings, err := settingsSvc.Get(context.Background())
	if err != nil {
		log.Fatalf("load settings: %v", err)
	}

	mihomoMgr := mihomo.NewManager(mihomo.Options{
		BinaryPath:          cfg.MihomoBinaryPath,
		RuntimeDir:          cfg.RuntimeDir,
		ProdConfigPath:      cfg.ProdConfigPath,
		ProbeConfigPath:     cfg.ProbeConfigPath,
		ProdControllerAddr:  cfg.ProdControllerAddr,
		ProbeControllerAddr: cfg.ProbeControllerAddr,
		ProbeMixedPort:      cfg.ProbeMixedPort,
		InitialLogLevel:     currentSettings.LogLevel,
	})
	mihomoInstaller := mihomo.NewInstaller(cfg.MihomoInstallDir, cfg.MihomoBinaryStatePath)
	poolSvc := pools.NewService(store, settingsSvc, nodeSvc, subSvc, mihomoMgr, broker)
	probeSvc := probe.NewService(settingsSvc, store, nodeSvc, subSvc, poolSvc, mihomoMgr, broker)

	// free proxy pipeline
	var freeStore freproxies.Store
	redisOK := false
	if cfg.FreeProxyEnabled {
		rs, err := freproxies.OpenRedis(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
		if err != nil {
			log.Printf("redis unavailable (%v), fallback to memory+sqlite for free proxies", err)
			freeStore = freproxies.PersistMemoryStore(freproxies.NewMemoryStore(), store)
		} else {
			freeStore = rs
			redisOK = true
			log.Printf("redis connected at %s", cfg.RedisAddr)
		}
	} else {
		freeStore = freproxies.PersistMemoryStore(freproxies.NewMemoryStore(), store)
	}
	registry := crawlers.NewRegistry(crawlers.DefaultSources())
	scraperSvc := scrapers.New(store, registry)
	freeSvc := freproxies.NewService(freeStore, registry, broker, redisOK)
	geoSvc := geoip.New(nil)
	freeSvc.SetGeoService(geoSvc)
	freeSvc.SetGeoLookup(func(ctx context.Context, ip string) (string, error) {
		r, err := geoSvc.Lookup(ctx, ip)
		if err != nil {
			return "", err
		}
		return geoip.FormatRegion(r), nil
	})
	freeSvc.StartGeoWorker(rootCtx)
	freeSvc.StartHotCache(rootCtx)

	blStore := blacklist.New(store.DB)
	auditStore := audit.New(store.DB)
	tokenStore := apitoken.New(store.DB)
	trafficHistStore := traffichist.New(store.DB)
	stickyStore := sticky.New(10 * time.Minute)

	freeSvc.SetBlockedFn(func(addr string) bool { return blStore.Blocked(addr) })
	freeSvc.SetSourceDisabledFn(func(source string) bool { return sourcestats.Default.IsDisabled(source) })

	// Per-channel temporary bans. The global blacklist above is the hard,
	// admin-owned ban; this one is automatic, per-destination and expiring.
	chanStore := chanpolicy.NewSQLStore(store.DB)
	channels := chanpolicy.New(chanpolicy.Options{
		Policy:  chanpolicy.Defaults(),
		Persist: chanStore,
		OnBan: func(b chanpolicy.Ban) {
			broker.Publish("channel_ban", b)
			webhook.Default.Notify("channel_ban", map[string]any{
				"channel": b.Channel, "addr": b.Addr, "reason": b.Reason,
				"until": b.Until, "strikes": b.Strikes,
			})
		},
		OnUnban: func(b chanpolicy.Ban) { broker.Publish("channel_unban", b) },
	})
	if restored, err := chanStore.LoadBans(); err != nil {
		log.Printf("channel bans: load failed: %v", err)
	} else if n := channels.Restore(restored); n > 0 {
		log.Printf("channel bans: restored %d active ban(s)", n)
	}
	if logs, err := chanStore.LoadOutcomes(500); err != nil {
		log.Printf("channel logs: load failed: %v", err)
	} else if len(logs) > 0 {
		channels.RestoreLogs(logs)
	}
	if allows, err := chanStore.LoadAllows(); err != nil {
		log.Printf("channel allowlist: load failed: %v", err)
	} else if len(allows) > 0 {
		channels.RestoreAllows(allows)
		log.Printf("channel allowlist: restored %d entries", len(allows))
	}
	if rules, err := chanStore.LoadRules(); err != nil {
		log.Printf("channel rules: load failed: %v", err)
	} else if len(rules) > 0 {
		channels.RestoreRules(rules)
		log.Printf("channel rules: restored %d", len(rules))
	}
	channels.StartSweeper(rootCtx, time.Minute)
	freeSvc.SetChannelPolicy(channels)

	poolSvc.SetFreeService(freeSvc)
	sourcestats.Default.Attach(store)
	valSvc := validator.New(cfg, freeSvc)
	valSvc.SetDB(store)
	sched := scheduler.New(cfg, freeSvc, valSvc)
	sched.SetIntervalProvider(func(ctx context.Context) (scrapeSec, validateSec int) {
		st, err := settingsSvc.Get(ctx)
		if err != nil {
			return cfg.ScrapeIntervalSec, cfg.ValidateIntervalSec
		}
		return st.ScrapeIntervalSec, st.ValidateIntervalSec
	})
	featCfg := features.Default()
	// apply persisted free/chain runtime from settings
	if st, err := settingsSvc.Get(rootCtx); err == nil {
		if st.ProxyChainHops >= 2 {
			cfg.ProxyChainHops = st.ProxyChainHops
		}
		if st.SessionMaxAgeSec > 0 {
			authSvc.SetSessionMaxAgeSec(st.SessionMaxAgeSec)
		}
		valSvc.ApplyFreeConfig(st.FreeValidateURL, st.FreeValidateTimeoutMS, st.FreeValidateConcurrency)
		featCfg = features.Parse(st.FeatureJSON)
		valSvc.ApplyExtras(featCfg.ValidateURLs(st.FreeValidateURL), featCfg.SourceAutoDisableRate, featCfg.SourceMinSamples)
		webhook.Default.Configure(featCfg.WebhookURL, featCfg.WebhookEvents)
		if featCfg.StickyTTLSec > 0 {
			stickyStore.SetTTL(time.Duration(featCfg.StickyTTLSec) * time.Second)
		}
		channels.SetPolicy(featCfg.Channels.ToPolicy())
		freeSvc.SetPickDefaults(featCfg.Channels.PickStrategy, featCfg.Channels.CooldownDuration())
		trafficHistStore.Start(rootCtx, time.Duration(featCfg.TrafficSampleSec)*time.Second, featCfg.TrafficRetainHours)
	} else {
		trafficHistStore.Start(rootCtx, time.Minute, 48)
	}
	sched.Start(rootCtx)

	direct := directproxy.New(directproxy.Config{
		ListenAddr:   cfg.DirectProxyAddr,
		Username:     cfg.DirectProxyUsername,
		Password:     cfg.DirectProxyPassword,
		Enabled:      cfg.DirectProxyEnabled && cfg.FreeProxyEnabled,
		ChainEnabled: cfg.ProxyChainEnabled && cfg.FreeProxyEnabled,
		ChainAddr:    cfg.ProxyChainAddr,
		ChainHops:    cfg.ProxyChainHops,
	}, freeSvc)
	direct.SetChannelPolicy(channels)
	direct.SetSticky(stickyStore, featCfg.DirectStickyEnabled)
	direct.SetAllowedCIDRs(featCfg.AllowedCIDRs)
	direct.SetAuthRequired(featCfg.DirectAuthRequired)
	direct.SetRateLimit(featCfg.RateLimitBytesPerSec)
	if err := direct.Start(rootCtx); err != nil {
		log.Printf("directproxy start skipped: %v", err)
	}

	subSvc.SetAfterSyncHook(func(ctx context.Context, subscriptionID int64, nodeIDs []int64) {
		_ = subscriptionID
		for _, nodeID := range nodeIDs {
			_ = probeSvc.EnqueueLatency("subscription", nodeID)
		}
	})
	subSvc.AddAfterSyncHook(func(ctx context.Context, subscriptionID int64, nodeIDs []int64) {
		_ = subscriptionID
		_ = nodeIDs
		publishCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := poolSvc.Publish(publishCtx, 0); err != nil {
			log.Printf("auto publish after sync failed: %v", err)
		}
	})
	if err := mihomoMgr.Start(context.Background(), currentSettings.MihomoControllerSecret); err != nil {
		log.Printf("mihomo start skipped: %v", err)
	}
	go func() {
		publishCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := poolSvc.Publish(publishCtx, 0); err != nil {
			log.Printf("startup publish failed: %v", err)
		}
	}()
	probeSvc.Start(rootCtx)
	subSvc.StartScheduler(rootCtx)

	webApp, err := web.New(authSvc, settingsSvc, nodeSvc, subSvc, poolSvc, probeSvc, mihomoMgr, mihomoInstaller, broker, freeSvc, sched, direct, scraperSvc, cfg, requestShutdown, web.FeatureDeps{
		Blacklist:   blStore,
		Audit:       auditStore,
		Tokens:      tokenStore,
		TrafficHist: trafficHistStore,
		Channels:    channels,
	})
	if err != nil {
		log.Fatalf("build web app: %v", err)
	}
	webApp.ApplyFeatureHot(featCfg.Marshal())
	router, err := webApp.Router()
	if err != nil {
		log.Fatalf("build router: %v", err)
	}

	listenAddr := currentSettings.PanelHost + ":" + strconv.Itoa(currentSettings.PanelPort)
	mux, err := proxy.NewMux(poolSvc, router, listenAddr)
	if err != nil {
		log.Fatalf("create proxy mux: %v", err)
	}

	go func() {
		log.Printf("unified-proxy-pool listening on %s (panel + proxy), free-proxy sources=%d backend=%s", listenAddr, len(registry.All()), freeStore.Backend())
		if err := mux.Serve(); err != nil {
			log.Fatalf("proxy mux error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		requestShutdown()
	}()

	<-rootCtx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mux.Shutdown(shutdownCtx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}
	direct.Stop()
	_ = freeStore.Close()
	mihomoMgr.Stop()
}
