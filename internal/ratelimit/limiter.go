package ratelimit

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ManiSaiTeja2007/aeroproxy/internal/metrics"
)

// TokenBucket holds the rate limiting state for a single IP.
type TokenBucket struct {
	capacity   float64
	tokens     float64
	rate       float64 // tokens per second
	lastRefill time.Time
}

// RateLimiter manages the token buckets for all client IPs.
type RateLimiter struct {
	mu           sync.RWMutex
	buckets      map[string]*TokenBucket
	blockedUntil map[string]time.Time
	rate         float64
	capacity     float64
	broadcastFn  func(ip string, blockedUntil time.Time)
}

// NewRateLimiter creates a new RateLimiter instance.
func NewRateLimiter(rate, capacity float64) *RateLimiter {
	return &RateLimiter{
		buckets:      make(map[string]*TokenBucket),
		blockedUntil: make(map[string]time.Time),
		rate:         rate,
		capacity:     capacity,
	}
}

// SetBroadcastCallback registers the function to call when an IP is blocked.
func (rl *RateLimiter) SetBroadcastCallback(fn func(ip string, blockedUntil time.Time)) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.broadcastFn = fn
}

// BlockIP records an IP block from the gossip network.
func (rl *RateLimiter) BlockIP(ip string, blockedUntil time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.blockedUntil == nil {
		rl.blockedUntil = make(map[string]time.Time)
	}
	if current, exists := rl.blockedUntil[ip]; !exists || blockedUntil.After(current) {
		rl.blockedUntil[ip] = blockedUntil
	}
}

// MergeBlocks merges a map of remote active IP blocks.
func (rl *RateLimiter) MergeBlocks(blocks map[string]time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.blockedUntil == nil {
		rl.blockedUntil = make(map[string]time.Time)
	}
	now := time.Now()
	for ip, until := range blocks {
		if now.Before(until) {
			if current, exists := rl.blockedUntil[ip]; !exists || until.After(current) {
				rl.blockedUntil[ip] = until
			}
		}
	}
}

// GetActiveBlocks returns a map of all currently active IP blocks.
func (rl *RateLimiter) GetActiveBlocks() map[string]time.Time {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	active := make(map[string]time.Time)
	now := time.Now()
	for ip, until := range rl.blockedUntil {
		if now.Before(until) {
			active[ip] = until
		}
	}
	return active
}

// Allow checks if the request from the given IP address is allowed.
// If allowed, it decrements the token count and returns true.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	if until, blocked := rl.blockedUntil[ip]; blocked && now.Before(until) {
		return false
	}

	bucket, exists := rl.buckets[ip]
	if !exists {
		bucket = &TokenBucket{
			capacity:   rl.capacity,
			tokens:     rl.capacity,
			rate:       rl.rate,
			lastRefill: now,
		}
		rl.buckets[ip] = bucket
	} else {
		elapsed := now.Sub(bucket.lastRefill).Seconds()
		refilled := elapsed * bucket.rate
		if refilled > 0 {
			bucket.tokens += refilled
			if bucket.tokens > bucket.capacity {
				bucket.tokens = bucket.capacity
			}
			bucket.lastRefill = now
		}
	}

	if bucket.tokens >= 1.0 {
		bucket.tokens -= 1.0
		return true
	}

	// Rate limit hit: block the IP locally for 5 seconds
	blockedUntil := now.Add(5 * time.Second)
	if rl.blockedUntil == nil {
		rl.blockedUntil = make(map[string]time.Time)
	}
	rl.blockedUntil[ip] = blockedUntil

	// Trigger broadcast callback asynchronously
	if rl.broadcastFn != nil {
		fn := rl.broadcastFn
		go fn(ip, blockedUntil)
	}

	return false
}

// Middleware intercepts HTTP requests and rate limits them based on client IP.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ip string
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if len(parts) > 0 {
				ip = strings.TrimSpace(parts[0])
			}
		}
		if ip == "" {
			if xri := r.Header.Get("X-Real-IP"); xri != "" {
				ip = strings.TrimSpace(xri)
			}
		}
		if ip == "" {
			var err error
			ip, _, err = net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
		}

		if !rl.Allow(ip) {
			metrics.BlocksTotal.Inc()
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
