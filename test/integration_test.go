package test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/ManiSaiTeja2007/aeroproxy/internal/health"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/logger"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/pipeline"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/proxy"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/ratelimit"
)

func TestMain(m *testing.M) {
	logger.InitNop()
	m.Run()
}

func TestIntegration(t *testing.T) {
	// 1. Spin up 3 dummy backend servers.
	var mu sync.Mutex
	backendHits := make(map[string]int)

	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		backendHits["server1"]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("server1"))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		backendHits["server2"]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("server2"))
	}))
	defer server2.Close()

	server3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		backendHits["server3"]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("server3"))
	}))
	defer server3.Close()

	// Parse URLs
	url1, _ := url.Parse(server1.URL)
	url2, _ := url.Parse(server2.URL)
	url3, _ := url.Parse(server3.URL)

	// Create pool and register backends
	pool := &proxy.ServerPool{}
	backends := []*proxy.Backend{
		{URL: url1, Alive: false},
		{URL: url2, Alive: false},
		{URL: url3, Alive: false},
	}

	for _, b := range backends {
		proxyHandler := httputil.NewSingleHostReverseProxy(b.URL)
		originalDirector := proxyHandler.Director
		proxyHandler.Director = func(req *http.Request) {
			originalDirector(req)
			req.Host = b.URL.Host
		}
		b.ReverseProxy = proxyHandler
		pool.AddBackend(b)
	}

	// Check health with retries to account for transient TCP binding delays on Windows
	var healthy bool
	for attempt := 0; attempt < 5; attempt++ {
		health.CheckHealthOnce(pool)
		allAlive := true
		for _, b := range pool.Backends {
			if !b.IsAlive() {
				allAlive = false
				break
			}
		}
		if allAlive {
			healthy = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !healthy {
		t.Fatalf("Not all backends are alive after health check attempts")
	}

	// Wrap the pool in a rate limiter (capacity = 2, rate = 0.1 tokens/sec to avoid fast refill during test)
	limiter := ratelimit.NewRateLimiter(0.1, 2.0)
	handler := limiter.Middleware(pool)

	// Test 1: Round Robin routing
	// Fire 3 requests, each from a unique IP, so we bypass rate limiter capacity limit of 2.
	clientIPs := []string{"192.168.1.1:1234", "192.168.1.2:1234", "192.168.1.3:1234"}
	responses := make(map[string]int)

	for _, ip := range clientIPs {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = ip
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		res := rec.Result()
		if res.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d from IP %s", res.StatusCode, ip)
		}

		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		responses[string(body)]++
	}

	// Assert each backend was hit exactly once
	mu.Lock()
	if len(backendHits) != 3 {
		t.Errorf("Expected 3 different backend servers to be hit, got %d", len(backendHits))
	}
	for name, count := range backendHits {
		if count != 1 {
			t.Errorf("Expected backend %s to be hit once, got %d times", name, count)
		}
	}
	mu.Unlock()

	// Assert that proxy response bodies matches round-robin distribution
	if responses["server1"] != 1 || responses["server2"] != 1 || responses["server3"] != 1 {
		t.Errorf("Unexpected round robin response distribution: %v", responses)
	}

	// Test 2: Rate Limiting
	// Fire 5 rapid requests from the same IP ("192.168.2.1:1234").
	// First 2 should return HTTP 200, and the remaining 3 should return HTTP 429.
	sameIP := "192.168.2.1:1234"
	var successCount, rateLimitCount int

	for i := 1; i <= 5; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = sameIP
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		res := rec.Result()
		res.Body.Close()

		if res.StatusCode == http.StatusOK {
			successCount++
		} else if res.StatusCode == http.StatusTooManyRequests {
			rateLimitCount++
		} else {
			t.Errorf("Request %d from same IP got unexpected status: %d", i, res.StatusCode)
		}
	}

	if successCount != 2 {
		t.Errorf("Expected exactly 2 successful requests (HTTP 200), got %d", successCount)
	}
	if rateLimitCount != 3 {
		t.Errorf("Expected exactly 3 rate limited requests (HTTP 429), got %d", rateLimitCount)
	}
}

func TestEncryptionPipeline(t *testing.T) {
	// Mock a backend server that reads the request body and parses the JSON
	var receivedBody string
	var mu sync.Mutex

	mockBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("Backend failed to read body: %v", err)
		}
		mu.Lock()
		receivedBody = string(body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer mockBackend.Close()

	// Parse URL
	backendURL, _ := url.Parse(mockBackend.URL)

	// Create pool and register mockBackend
	pool := &proxy.ServerPool{}
	backend := &proxy.Backend{
		URL:   backendURL,
		Alive: true,
	}

	proxyHandler := httputil.NewSingleHostReverseProxy(backend.URL)
	originalDirector := proxyHandler.Director
	proxyHandler.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = backend.URL.Host
	}
	backend.ReverseProxy = proxyHandler
	pool.AddBackend(backend)

	// Wrap in pipeline DataShifterMiddleware
	handler := pipeline.DataShifterMiddleware(pool)

	// Create a POST request with plain JSON
	jsonPayload := `{"email": "test@example.com", "name": "Mani"}`
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.168.3.1:1234"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", res.StatusCode)
	}

	mu.Lock()
	bodyStr := receivedBody
	mu.Unlock()

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(bodyStr), &data); err != nil {
		t.Fatalf("Failed to unmarshal received body: %v (body: %s)", err, bodyStr)
	}

	// Assertions: 'name' should remain plain, 'email' should be encrypted
	nameVal, ok := data["name"]
	if !ok || nameVal != "Mani" {
		t.Errorf("Expected name 'Mani', got: %v", nameVal)
	}

	emailVal, ok := data["email"]
	if !ok {
		t.Fatalf("email field missing in decrypted JSON")
	}

	emailStr, ok := emailVal.(string)
	if !ok {
		t.Fatalf("email field is not a string: %v", emailVal)
	}

	if emailStr == "test@example.com" {
		t.Errorf("Expected email to be encrypted, but got plain text: %s", emailStr)
	}

	// Try base64 decoding the email field to verify it is a valid base64 string
	cipherBytes, err := base64.StdEncoding.DecodeString(emailStr)
	if err != nil {
		t.Errorf("Expected email to be base64 encoded string, decoding failed: %v", err)
	}

	// Verify encryption ciphertext has appropriate length
	if len(cipherBytes) < 28 {
		t.Errorf("Cipher text length too short: %d bytes", len(cipherBytes))
	}
}
