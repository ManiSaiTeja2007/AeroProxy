package proxy

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// BreakerState defines the states of the circuit breaker state machine.
type BreakerState int

const (
	StateClosed BreakerState = iota
	StateOpen
	StateHalfOpen
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
	TripDuration      time.Duration
	BreakerState      BreakerState
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
	b.mux.Lock()
	defer b.mux.Unlock()
	if !b.Alive {
		return false
	}

	now := time.Now()
	if b.BreakerState == StateOpen {
		if now.After(b.TrippedUntil) {
			b.BreakerState = StateHalfOpen
			return true
		}
		return false
	}

	if b.BreakerState == StateHalfOpen {
		// Limit to exactly 1 concurrent probe request
		return atomic.LoadInt64(&b.ActiveRequests) == 0
	}

	return true
}

// IsTripped returns true if the circuit breaker is currently tripped.
func (b *Backend) IsTripped() bool {
	b.mux.RLock()
	defer b.mux.RUnlock()
	return b.BreakerState == StateOpen
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

// EnsureWarmup initializes EWMA to 50ms so new servers aren't overloaded ("too fast").
// Seeding new backends prevents "0-score bias" where a new server with 0 active requests
// and 0 EWMA receives 100% of traffic and gets immediately overloaded.
func (b *Backend) EnsureWarmup() {
	b.mux.Lock()
	defer b.mux.Unlock()
	if b.EWMA == 0 {
		b.EWMA = 50.0 // Warmup value
	}
}

// RecordError increments consecutive errors and trips circuit if threshold is reached.
func (b *Backend) RecordError() {
	b.mux.Lock()
	defer b.mux.Unlock()
	b.ConsecutiveErrors++
	if b.BreakerState == StateHalfOpen || b.ConsecutiveErrors >= 3 {
		b.BreakerState = StateOpen
		timeout := b.TripDuration
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		b.TrippedUntil = time.Now().Add(timeout)
	}
}

// RecordSuccess resets consecutive errors to zero.
func (b *Backend) RecordSuccess() {
	b.mux.Lock()
	defer b.mux.Unlock()
	b.ConsecutiveErrors = 0
	if b.BreakerState == StateHalfOpen {
		b.BreakerState = StateClosed
	}
}

// ServerPool tracks multiple backends and the current load balancing state.
type ServerPool struct {
	backends atomic.Value // Stores []*Backend
	writeMux sync.Mutex   // Protects modifications
	current  uint64
}

// AddBackend adds a backend to the server pool.
func (s *ServerPool) AddBackend(backend *Backend) {
	s.writeMux.Lock()
	defer s.writeMux.Unlock()

	var current []*Backend
	val := s.backends.Load()
	if val != nil {
		current = val.([]*Backend)
	}

	newBackends := make([]*Backend, len(current)+1)
	copy(newBackends, current)
	newBackends[len(current)] = backend

	s.backends.Store(newBackends)
}

// GetBackends returns the slice of backends stored in the atomic value.
func (s *ServerPool) GetBackends() []*Backend {
	val := s.backends.Load()
	if val == nil {
		return nil
	}
	return val.([]*Backend)
}

// GetScore returns the routing score (EWMA * (ActiveRequests + 1)).
func (b *Backend) GetScore() float64 {
	b.EnsureWarmup()
	active := atomic.LoadInt64(&b.ActiveRequests)
	b.mux.RLock()
	ewma := b.EWMA
	b.mux.RUnlock()
	return ewma * float64(active+1)
}

// GetNextPeer selects the next healthy backend using Power of Two Choices (P2C) and EWMA/active requests scoring.
func (s *ServerPool) GetNextPeer() *Backend {
	backends := s.GetBackends()
	n := len(backends)
	if n == 0 {
		return nil
	}
	if n == 1 {
		if backends[0].IsHealthy() {
			return backends[0]
		}
		return nil
	}

	// 1. Try Power of Two Choices (P2C)
	// Pick 2 random distinct indices
	i1 := rand.Intn(n)
	i2 := rand.Intn(n)
	for i1 == i2 {
		i2 = rand.Intn(n)
	}

	b1 := backends[i1]
	b2 := backends[i2]

	h1 := b1.IsHealthy()
	h2 := b2.IsHealthy()

	// If both chosen are healthy, compare their scores
	if h1 && h2 {
		if b1.GetScore() <= b2.GetScore() {
			return b1
		}
		return b2
	}
	// If only one is healthy, return it
	if h1 {
		return b1
	}
	if h2 {
		return b2
	}

	// 2. Fallback: If both chosen are unhealthy, scan all backends to find a healthy one.
	// This ensures we don't return nil if a healthy node exists.
	var bestPeer *Backend
	var bestScore float64
	hasBest := false

	for _, b := range backends {
		if !b.IsHealthy() {
			continue
		}
		score := b.GetScore()
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

// RegisterBackendURL parses a raw URL string, constructs a Backend, sets up its ReverseProxy and Transport,
// and registers it to the server pool.
func RegisterBackendURL(pool *ServerPool, rawURL string, tripDuration time.Duration) (*Backend, error) {
	serverURL, err := url.Parse(rawURL)
	if err != nil || serverURL.Scheme == "" || serverURL.Host == "" {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	proxyHandler := httputil.NewSingleHostReverseProxy(serverURL)
	originalDirector := proxyHandler.Director
	proxyHandler.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = serverURL.Host
	}

	backend := &Backend{
		URL:          serverURL,
		Alive:        true,
		ReverseProxy: proxyHandler,
		TripDuration: tripDuration,
	}

	proxyHandler.Transport = &TrackingTransport{
		Backend:   backend,
		Transport: http.DefaultTransport,
	}

	pool.AddBackend(backend)
	return backend, nil
}
