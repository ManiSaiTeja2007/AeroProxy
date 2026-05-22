package health

import (
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
	for _, backend := range pool.Backends {
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

// CheckHealth runs an infinite loop checking backend status every 10 seconds.
func CheckHealth(pool *proxy.ServerPool) {
	for {
		CheckHealthOnce(pool)
		time.Sleep(10 * time.Second)
	}
}
