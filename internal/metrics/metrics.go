package metrics

import (
	"errors"
	"net/http"
	"strconv"
	"sync"

	"github.com/ManiSaiTeja2007/aeroproxy/internal/logger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

var (
	// RequestsTotal tracks the total requests handled by the proxy, partitioned by status code.
	RequestsTotal *prometheus.CounterVec

	// BlocksTotal tracks requests blocked with HTTP 429.
	BlocksTotal prometheus.Counter

	// EncryptionLatency tracks JSON encryption duration in milliseconds.
	EncryptionLatency prometheus.Histogram

	registerOnce sync.Once
)

// InitMetrics registers the metrics with Prometheus, ignoring AlreadyRegisteredError for testing safety.
func InitMetrics() {
	registerOnce.Do(func() {
		RequestsTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "aeroproxy_requests_total",
				Help: "Total number of HTTP requests processed by AeroProxy",
			},
			[]string{"status_code"},
		)

		BlocksTotal = prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "aeroproxy_429_blocks_total",
				Help: "Total number of requests blocked by rate limiter (429)",
			},
		)

		EncryptionLatency = prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "aeroproxy_encryption_latency_ms",
				Help:    "Latency of JSON encryption in milliseconds",
				Buckets: prometheus.DefBuckets,
			},
		)

		mustRegister(RequestsTotal)
		mustRegister(BlocksTotal)
		mustRegister(EncryptionLatency)
	})
}

func init() {
	InitMetrics()
}

func mustRegister(c prometheus.Collector) {
	err := prometheus.Register(c)
	if err != nil {
		var alreadyRegErr prometheus.AlreadyRegisteredError
		if errors.As(err, &alreadyRegErr) {
			return
		}
		panic(err)
	}
}

// StartMetricsServer boots up a metrics endpoint on a dedicated address.
func StartMetricsServer(addr string) {
	InitMetrics()
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		logger.Log.Info("Starting metrics server", zap.String("addr", addr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Log.Error("Metrics server failed", zap.Error(err))
		}
	}()
}

type responseWriterInterceptor struct {
	http.ResponseWriter
	statusCode int
}

func (w *responseWriterInterceptor) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// MetricsMiddleware wraps handlers to intercept status codes and increment requests count.
func MetricsMiddleware(next http.Handler) http.Handler {
	InitMetrics()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		interceptor := &responseWriterInterceptor{
			ResponseWriter: w,
			statusCode:     http.StatusOK, // default to 200 OK
		}

		next.ServeHTTP(interceptor, r)

		RequestsTotal.WithLabelValues(strconv.Itoa(interceptor.statusCode)).Inc()
	})
}
