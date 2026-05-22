package health

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/ManiSaiTeja2007/aeroproxy/internal/logger"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/proxy"
	"go.uber.org/zap"
)

// getTCPAddress returns the host and port string for TCP connection.
func getTCPAddress(u *url.URL) string {
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		h := u.Host
		if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
			h = h[1 : len(h)-1]
		}
		if u.Scheme == "https" {
			return net.JoinHostPort(h, "443")
		}
		return net.JoinHostPort(h, "80")
	}
	return net.JoinHostPort(host, port)
}

// CheckHealthOnce performs a single round of TCP dial checks on all pool backends.
func CheckHealthOnce(pool *proxy.ServerPool) {
	for _, backend := range pool.GetBackends() {
		// Detect circuit-breaker state transitions
		isTripped := backend.IsTripped()
		wasTripped := backend.GetLastTrippedState()
		if isTripped != wasTripped {
			if isTripped {
				logger.Log.Warn("Circuit Breaker Tripped", zap.String("url", backend.URL.String()))
			} else {
				logger.Log.Info("Circuit Breaker Healed", zap.String("url", backend.URL.String()))
			}
			backend.SetLastTrippedState(isTripped)
		}

		addr := getTCPAddress(backend.URL)
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			logger.Log.Error("Backend health check failed",
				zap.String("backend", backend.URL.String()),
				zap.Error(err),
			)
			backend.SetAlive(false)
		} else {
			logger.Log.Info("Backend health check succeeded",
				zap.String("backend", backend.URL.String()),
			)
			backend.SetAlive(true)
			conn.Close()
		}
	}
}

// CheckHealth runs a loop checking backend status every 10 seconds until context is cancelled.
func CheckHealth(ctx context.Context, pool *proxy.ServerPool) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	// Initial check
	CheckHealthOnce(pool)

	for {
		select {
		case <-ctx.Done():
			logger.Log.Info("Health checker loop stopping")
			return
		case <-ticker.C:
			CheckHealthOnce(pool)
		}
	}
}
