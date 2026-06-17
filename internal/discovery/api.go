package discovery

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ManiSaiTeja2007/aeroproxy/internal/logger"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/proxy"
	"go.uber.org/zap"
)

// AddBackendRequest represents the schema for adding a backend.
type AddBackendRequest struct {
	URL string `json:"url"`
}

// RegisterDiscoveryAPI wires the POST /backends/add handler to the provided mux.
// TODO: Production API should be secured via mTLS or shared-secret headers.
func RegisterDiscoveryAPI(mux *http.ServeMux, pool *proxy.ServerPool, onAdd func(string)) {
	mux.HandleFunc("/backends/list", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		backends := pool.GetBackends()
		list := make([]string, 0, len(backends))
		for _, b := range backends {
			list = append(list, b.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	})

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

		var timeout time.Duration
		backends := pool.GetBackends()
		if len(backends) > 0 {
			timeout = backends[0].TripDuration
		}

		_, err := proxy.RegisterBackendURL(pool, req.URL, timeout)
		if err != nil {
			logger.Log.Error("Invalid backend URL in request", zap.String("url", req.URL), zap.Error(err))
			http.Error(w, "Invalid URL", http.StatusBadRequest)
			return
		}

		logger.Log.Info("Backend server dynamically registered via API", zap.String("url", req.URL))

		if onAdd != nil {
			onAdd(req.URL)
		}

		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("Backend added successfully"))
	})
}
