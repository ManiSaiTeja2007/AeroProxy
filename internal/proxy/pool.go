package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
)

// Backend represents a backend server to proxy requests to.
type Backend struct {
	URL          *url.URL
	Alive        bool
	mux          sync.RWMutex
	ReverseProxy *httputil.ReverseProxy
}

// SetAlive sets the alive status of the backend in a thread-safe manner.
func (b *Backend) SetAlive(alive bool) {
	b.mux.Lock()
	defer b.mux.Unlock()
	b.Alive = alive
}

// IsAlive returns the alive status of the backend in a thread-safe manner.
func (b *Backend) IsAlive() bool {
	b.mux.RLock()
	defer b.mux.RUnlock()
	return b.Alive
}

// ServerPool tracks multiple backends and the current load balancing state.
type ServerPool struct {
	Backends []*Backend
	current  uint64
}

// AddBackend adds a backend to the server pool.
func (s *ServerPool) AddBackend(backend *Backend) {
	s.Backends = append(s.Backends, backend)
}

// GetNextPeer selects the next healthy backend using Round-Robin routing.
func (s *ServerPool) GetNextPeer() *Backend {
	n := len(s.Backends)
	if n == 0 {
		return nil
	}

	next := atomic.AddUint64(&s.current, 1)
	for i := 0; i < n; i++ {
		idx := (next + uint64(i)) % uint64(n)
		peer := s.Backends[idx]
		if peer.IsAlive() {
			return peer
		}
	}
	return nil
}

// ServeHTTP acts as the HTTP handler, forwarding requests to the next available backend.
func (s *ServerPool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	peer := s.GetNextPeer()
	if peer == nil {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	peer.ReverseProxy.ServeHTTP(w, r)
}
