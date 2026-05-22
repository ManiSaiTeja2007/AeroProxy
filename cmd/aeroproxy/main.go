package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/ManiSaiTeja2007/aeroproxy/internal/config"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/health"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/logger"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/metrics"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/pipeline"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/proxy"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/ratelimit"
	"go.uber.org/zap"
)

func main() {
	// Initialize Zap logger
	if err := logger.InitLogger(); err != nil {
		panic("Failed to initialize logger: " + err.Error())
	}
	defer func() {
		_ = logger.Log.Sync()
	}()

	logger.Log.Info("AeroProxy starting up...")

	// Load config
	cfg, err := config.LoadConfig("")
	if err != nil {
		logger.Log.Fatal("Failed to load configuration", zap.Error(err))
	}
	logger.Log.Info("Configuration loaded successfully",
		zap.String("port", cfg.Server.Port),
		zap.Strings("backends", cfg.Server.Backends),
	)

	// Start Prometheus metrics server on isolated port 9090
	metrics.StartMetricsServer(":9090")

	// Initialize server pool
	pool := &proxy.ServerPool{}

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
		pool.AddBackend(backend)
		logger.Log.Info("Backend server registered", zap.String("url", rawURL))
	}

	// Start active health checking routine
	go health.CheckHealth(pool)

	// Construct rates and middlewares
	limiter := ratelimit.NewRateLimiter(cfg.RateLimit.Rate, cfg.RateLimit.Capacity)

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

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Log.Fatal("Server failed to run", zap.Error(err))
	}
}
