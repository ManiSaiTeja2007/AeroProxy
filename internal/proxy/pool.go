package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// Backend represents a backend server to proxy requests to.
type Backend struct {
	URL               *url.URL
	Alive             bool
	ActiveRequests    int64
	EWMA              float64
	ConsecutiveErrors int64
	TrippedUntil      time.Time
	LastTrippedState  bool
	mux               sync.RWMutex
	ReverseProxy      *httputil.ReverseProxy
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

// IsHealthy returns true if the backend is alive and the circuit breaker is not tripped.
func (b *Backend) IsHealthy() bool {
	b.mux.RLock()
	defer b.mux.RUnlock()
	return b.Alive && (b.TrippedUntil.IsZero() || time.Now().After(b.TrippedUntil))
}

// IsTripped returns true if the circuit breaker is currently tripped.
func (b *Backend) IsTripped() bool {
	b.mux.RLock()
	defer b.mux.RUnlock()
	return !b.TrippedUntil.IsZero() && time.Now().Before(b.TrippedUntil)
}

// GetLastTrippedState returns the last recorded tripped state.
func (b *Backend) GetLastTrippedState() bool {
	b.mux.RLock()
	defer b.mux.RUnlock()
	return b.LastTrippedState
}

// SetLastTrippedState sets the last recorded tripped state.
func (b *Backend) SetLastTrippedState(state bool) {
	b.mux.Lock()
	defer b.mux.Unlock()
	b.LastTrippedState = state
}

// IncActive increments the active request count.
func (b *Backend) IncActive() {
	atomic.AddInt64(&b.ActiveRequests, 1)
}

// DecActive decrements the active request count.
func (b *Backend) DecActive() {
	atomic.AddInt64(&b.ActiveRequests, -1)
}

// UpdateEWMA calculates the exponentially weighted moving average latency.
func (b *Backend) UpdateEWMA(durationMs float64) {
	b.mux.Lock()
	defer b.mux.Unlock()
	if b.EWMA == 0 {
		b.EWMA = durationMs
	} else {
		b.EWMA = (b.EWMA * 0.8) + (durationMs * 0.2)
	}
}

// RecordError increments consecutive errors and trips circuit if threshold is reached.
func (b *Backend) RecordError() {
	b.mux.Lock()
	defer b.mux.Unlock()
	b.ConsecutiveErrors++
	if b.ConsecutiveErrors >= 3 {
		b.TrippedUntil = time.Now().Add(30 * time.Second)
	}
}

// RecordSuccess resets consecutive errors to zero.
func (b *Backend) RecordSuccess() {
	b.mux.Lock()
	defer b.mux.Unlock()
	b.ConsecutiveErrors = 0
}

// ServerPool tracks multiple backends and the current load balancing state.
type ServerPool struct {
	Backends []*Backend
	mux      sync.RWMutex
	current  uint64
}

// AddBackend adds a backend to the server pool.
func (s *ServerPool) AddBackend(backend *Backend) {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.Backends = append(s.Backends, backend)
}

// GetBackends returns a thread-safe copy of the backends slice.
func (s *ServerPool) GetBackends() []*Backend {
	s.mux.RLock()
	defer s.mux.RUnlock()
	backends := make([]*Backend, len(s.Backends))
	copy(backends, s.Backends)
	return backends
}

// GetNextPeer selects the next healthy backend using EWMA and active requests scoring.
func (s *ServerPool) GetNextPeer() *Backend {
	backends := s.GetBackends()
	if len(backends) == 0 {
		return nil
	}

	var bestPeer *Backend
	var bestScore float64
	hasBest := false

	for _, b := range backends {
		if !b.IsHealthy() {
			continue
		}

		active := atomic.LoadInt64(&b.ActiveRequests)
		b.mux.RLock()
		ewma := b.EWMA
		b.mux.RUnlock()

		score := ewma * float64(active+1)
		if !hasBest || score < bestScore {
			bestPeer = b
			bestScore = score
			hasBest = true
		}
	}

	return bestPeer
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
