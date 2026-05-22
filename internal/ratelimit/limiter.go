package ratelimit

import (
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
	mu       sync.Mutex
	buckets  map[string]*TokenBucket
	rate     float64
	capacity float64
}

// NewRateLimiter creates a new RateLimiter instance.
func NewRateLimiter(rate, capacity float64) *RateLimiter {
	return &RateLimiter{
		buckets:  make(map[string]*TokenBucket),
		rate:     rate,
		capacity: capacity,
	}
}

// Allow checks if the request from the given IP address is allowed.
// If allowed, it decrements the token count and returns true.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	bucket, exists := rl.buckets[ip]
	now := time.Now()
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
	return false
}

// Middleware intercepts HTTP requests and rate limits them based on client IP.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.RemoteAddr, ":")
		ip := parts[0]

		if !rl.Allow(ip) {
			metrics.BlocksTotal.Inc()
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
