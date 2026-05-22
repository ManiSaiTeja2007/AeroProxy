package discovery

import (
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/ManiSaiTeja2007/aeroproxy/internal/logger"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/proxy"
	"go.uber.org/zap"
)

// AddBackendRequest represents the schema for adding a backend.
type AddBackendRequest struct {
	URL string `json:"url"`
}

// RegisterDiscoveryAPI wires the POST /backends/add handler to the provided mux.
func RegisterDiscoveryAPI(mux *http.ServeMux, pool *proxy.ServerPool) {
	mux.HandleFunc("/backends/add", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		var req AddBackendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logger.Log.Error("Failed to decode add backend request", zap.Error(err))
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}

		serverURL, err := url.Parse(req.URL)
		if err != nil || serverURL.Scheme == "" || serverURL.Host == "" {
			logger.Log.Error("Invalid backend URL in request", zap.String("url", req.URL), zap.Error(err))
			http.Error(w, "Invalid URL", http.StatusBadRequest)
			return
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

		// Inject TrackingTransport for latency & circuit breaking tracking
		proxyHandler.Transport = &proxy.TrackingTransport{
			Backend:   backend,
			Transport: http.DefaultTransport,
		}

		pool.AddBackend(backend)
		logger.Log.Info("Backend server dynamically registered via API", zap.String("url", req.URL))

		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("Backend added successfully"))
	})
}
