package main

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ManiSaiTeja2007/aeroproxy/internal/cluster"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/config"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/discovery"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/health"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/logger"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/metrics"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/pipeline"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/proxy"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/ratelimit"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

func main() {
	// Load config
	cfg, err := config.LoadConfig("")
	if err != nil {
		panic("Failed to load configuration: " + err.Error())
	}

	// Initialize Zap logger
	if err := logger.InitLogger(cfg.LogLevel); err != nil {
		panic("Failed to initialize logger: " + err.Error())
	}
	defer func() {
		_ = logger.Log.Sync()
	}()

	logger.Log.Info("AeroProxy starting up...")
	logger.Log.Info("Configuration loaded successfully",
		zap.String("port", cfg.Server.Port),
		zap.Strings("backends", cfg.Server.Backends),
	)

	// Initialize server pool
	pool := &proxy.ServerPool{}

	// Construct rates and middlewares
	limiter := ratelimit.NewRateLimiter(cfg.RateLimit.Rate, cfg.RateLimit.Capacity)

	// Start Gossip Service for cluster rate-limiting sync
	gossipService, err := cluster.StartGossipService(
		cfg.Cluster.NodeName,
		cfg.Cluster.BindAddr,
		cfg.Cluster.BindPort,
		cfg.Cluster.JoinAddress,
		limiter,
	)
	if err != nil {
		logger.Log.Fatal("Failed to start Gossip Service", zap.Error(err))
	}

	// Start Prometheus metrics & Discovery control plane server on isolated port 9090
	mgmtMux := http.NewServeMux()
	metrics.InitMetrics()
	mgmtMux.Handle("/metrics", promhttp.Handler())
	discovery.RegisterDiscoveryAPI(mgmtMux, pool)

	mgmtServer := &http.Server{
		Addr:    ":9090",
		Handler: mgmtMux,
	}

	go func() {
		logger.Log.Info("Starting management control plane server", zap.String("addr", ":9090"))
		if err := mgmtServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Log.Error("Management server failed to run", zap.Error(err))
		}
	}()

	// Load initial backends from configuration
	for _, rawURL := range cfg.Server.Backends {
		serverURL, err := url.Parse(rawURL)
		if err != nil {
			logger.Log.Fatal("Failed to parse backend URL",
				zap.String("url", rawURL),
				zap.Error(err),
			)
		}

		proxyHandler := httputil.NewSingleHostReverseProxy(serverURL)

		originalDirector := proxyHandler.Director
		proxyHandler.Director = func(req *http.Request) {
			originalDirector(req)
			req.Host = serverURL.Host
		}

		backend := &proxy.Backend{
			URL:          serverURL,
			Alive:        true,
			ReverseProxy: proxyHandler,
		}

		// Inject TrackingTransport
		proxyHandler.Transport = &proxy.TrackingTransport{
			Backend:   backend,
			Transport: http.DefaultTransport,
		}

		pool.AddBackend(backend)
		logger.Log.Info("Backend server registered", zap.String("url", rawURL))
	}

	// Start active health checking routine
	healthCtx, cancelHealth := context.WithCancel(context.Background())
	go health.CheckHealth(healthCtx, pool)

	// Middlewares chain: Metrics -> RateLimiter -> DataShifter -> Pool
	handler := metrics.MetricsMiddleware(
		limiter.Middleware(
			pipeline.DataShifterMiddleware(pool),
		),
	)

	logger.Log.Info("Starting AeroProxy gateway server", zap.String("addr", cfg.Server.Port))
	server := &http.Server{
		Addr:    cfg.Server.Port,
		Handler: handler,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Log.Fatal("Server failed to run", zap.Error(err))
		}
	}()

	// Signal listener for graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	logger.Log.Info("Shutting down AeroProxy node...")

	// Cancel health checking loop
	cancelHealth()

	// Shutdown gossip cluster membership
	if gossipService != nil {
		if err := gossipService.Shutdown(); err != nil {
			logger.Log.Error("Gossip service shutdown error", zap.Error(err))
		}
	}

	// Graceful shutdown context with 5s timeout
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Log.Error("Gateway server shutdown failed", zap.Error(err))
		}
	}()
	go func() {
		defer wg.Done()
		if err := mgmtServer.Shutdown(shutdownCtx); err != nil {
			logger.Log.Error("Management server shutdown failed", zap.Error(err))
		}
	}()
	wg.Wait()

	logger.Log.Info("AeroProxy exited cleanly")
}
