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

	"github.com/ManiSaiTeja2007/aeroproxy/internal/cluster"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/discovery"
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
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		backendHits["server1"]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("server1"))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		backendHits["server2"]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("server2"))
	}))
	defer server2.Close()

	server3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
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
		b := b // Shadow loop variable for closure capture safety
		proxyHandler := httputil.NewSingleHostReverseProxy(b.URL)
		originalDirector := proxyHandler.Director
		proxyHandler.Director = func(req *http.Request) {
			originalDirector(req)
			req.Host = b.URL.Host
		}
		b.ReverseProxy = proxyHandler

		// Inject TrackingTransport
		proxyHandler.Transport = &proxy.TrackingTransport{
			Backend:   b,
			Transport: http.DefaultTransport,
		}

		pool.AddBackend(b)
	}

	// Check health with retries to account for transient TCP binding delays on Windows
	var healthy bool
	for attempt := 0; attempt < 5; attempt++ {
		health.CheckHealthOnce(pool)
		allAlive := true
		for _, b := range pool.GetBackends() {
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

	// Test 1: Predictive routing distribution
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

	if responses["server1"] != 1 || responses["server2"] != 1 || responses["server3"] != 1 {
		t.Errorf("Unexpected predictive routing response distribution: %v", responses)
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

	// Inject TrackingTransport
	proxyHandler.Transport = &proxy.TrackingTransport{
		Backend:   backend,
		Transport: http.DefaultTransport,
	}

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

func TestCircuitBreaker(t *testing.T) {
	// Spin up a failing server (returns 500) and a healthy fallback server.
	var failingHits, healthyHits int
	var mu sync.Mutex

	failingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		failingHits++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failing"))
	}))
	defer failingServer.Close()

	healthyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		healthyHits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("healthy"))
	}))
	defer healthyServer.Close()

	failingURL, _ := url.Parse(failingServer.URL)
	healthyURL, _ := url.Parse(healthyServer.URL)

	pool := &proxy.ServerPool{}

	// Setup failing backend
	failingBackend := &proxy.Backend{
		URL:   failingURL,
		Alive: true,
	}
	failingProxy := httputil.NewSingleHostReverseProxy(failingURL)
	failingProxy.Director = func(req *http.Request) {
		req.URL.Scheme = failingURL.Scheme
		req.URL.Host = failingURL.Host
		req.Host = failingURL.Host
	}
	failingProxy.Transport = &proxy.TrackingTransport{
		Backend:   failingBackend,
		Transport: http.DefaultTransport,
	}
	failingBackend.ReverseProxy = failingProxy
	pool.AddBackend(failingBackend)

	// Setup healthy backend
	healthyBackend := &proxy.Backend{
		URL:   healthyURL,
		Alive: true,
	}
	healthyProxy := httputil.NewSingleHostReverseProxy(healthyURL)
	healthyProxy.Director = func(req *http.Request) {
		req.URL.Scheme = healthyURL.Scheme
		req.URL.Host = healthyURL.Host
		req.Host = healthyURL.Host
	}
	healthyProxy.Transport = &proxy.TrackingTransport{
		Backend:   healthyBackend,
		Transport: http.DefaultTransport,
	}
	healthyBackend.ReverseProxy = healthyProxy
	pool.AddBackend(healthyBackend)

	// Set failingBackend EWMA to a very small non-zero value, and healthyBackend to slightly higher,
	// so that failingBackend is initially preferred (lower score) until it trips.
	failingBackend.UpdateEWMA(1.0)
	healthyBackend.UpdateEWMA(10.0)

	// Fire 3 requests to trip the circuit on failingBackend
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		pool.ServeHTTP(rec, req)
		res := rec.Result()
		res.Body.Close()

		if res.StatusCode != http.StatusInternalServerError {
			t.Errorf("Expected status 500, got %d", res.StatusCode)
		}
	}

	mu.Lock()
	fH := failingHits
	mu.Unlock()
	if fH != 3 {
		t.Fatalf("Expected failing server to be hit 3 times, got %d", fH)
	}

	// Verify that failingBackend is now tripped (IsHealthy returns false)
	if failingBackend.IsHealthy() {
		t.Fatalf("Expected failing backend to be unhealthy/tripped")
	}

	// Fire a 4th request. Since failingBackend is tripped, it must route to healthyBackend.
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	pool.ServeHTTP(rec, req)
	res := rec.Result()
	res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 from healthy fallback, got %d", res.StatusCode)
	}

	mu.Lock()
	hH := healthyHits
	mu.Unlock()
	if hH != 1 {
		t.Errorf("Expected healthy server to be hit once, got %d", hH)
	}
}

func TestDynamicDiscovery(t *testing.T) {
	// Spin up a mock backend server that will be registered dynamically
	var hitCount int
	var mu sync.Mutex

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hitCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("dynamic"))
	}))
	defer mockServer.Close()

	pool := &proxy.ServerPool{}
	// Initially pool is empty, so requests should fail with 503
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	pool.ServeHTTP(rec, req)
	if rec.Result().StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Expected 503 Service Unavailable for empty pool, got %d", rec.Result().StatusCode)
	}

	// Create the management mux and register discovery API
	mgmtMux := http.NewServeMux()
	discovery.RegisterDiscoveryAPI(mgmtMux, pool)

	// Fire POST /backends/add to add the mockServer URL
	addReqBody := `{"url": "` + mockServer.URL + `"}`
	addReq := httptest.NewRequest("POST", "/backends/add", bytes.NewBufferString(addReqBody))
	addReq.Header.Set("Content-Type", "application/json")
	addRec := httptest.NewRecorder()

	mgmtMux.ServeHTTP(addRec, addReq)

	addRes := addRec.Result()
	addRes.Body.Close()
	if addRes.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status 201 Created from discovery API, got %d", addRes.StatusCode)
	}

	// Now fire a request to the pool and assert it routes to the dynamically added backend
	req2 := httptest.NewRequest("GET", "/", nil)
	rec2 := httptest.NewRecorder()
	pool.ServeHTTP(rec2, req2)

	res2 := rec2.Result()
	body, _ := io.ReadAll(res2.Body)
	res2.Body.Close()

	if res2.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", res2.StatusCode)
	}
	if string(body) != "dynamic" {
		t.Errorf("Expected response body 'dynamic', got %s", string(body))
	}

	mu.Lock()
	hits := hitCount
	mu.Unlock()
	if hits != 1 {
		t.Errorf("Expected mock server to receive 1 hit, got %d", hits)
	}
}

func TestClusterSync(t *testing.T) {
	// Initialize two rate limiters (rate=1.0, capacity=2.0)
	limiterA := ratelimit.NewRateLimiter(1.0, 2.0)
	limiterB := ratelimit.NewRateLimiter(1.0, 2.0)

		// Set up GossipDelegate for Node B
	delegateB := &cluster.GossipDelegate{
		Limiter: limiterB,
	}

	// Mock GossipService A's broadcast callback to route it directly to Delegate B's NotifyMsg.
	// This tests the exact workflow: Rate limit triggers on A -> calls broadcast callback ->
	// marshals state -> Gossip layer delivers -> B's NotifyMsg receives & parses -> B blocks IP.
	limiterA.SetBroadcastCallback(func(ip string, blockedUntil time.Time) {
		event := cluster.BlockBroadcast{
			IP:           ip,
			BlockedUntil: blockedUntil,
		}
		data, err := json.Marshal(&event)
		if err != nil {
			t.Errorf("Failed to marshal block event: %v", err)
			return
		}
		delegateB.NotifyMsg(data)
	})

	testIP := "192.168.200.1"

	// Call Allow 3 times on Limiter A. Capacity is 2.0, so the 3rd should return false and block.
	if !limiterA.Allow(testIP) {
		t.Errorf("First call to limiterA should be allowed")
	}
	if !limiterA.Allow(testIP) {
		t.Errorf("Second call to limiterA should be allowed")
	}
	if limiterA.Allow(testIP) {
		t.Errorf("Third call to limiterA should be blocked")
	}

	// Wait for Node B to receive the blocked state (due to async goroutine callback)
	var blockedOnB bool
	for i := 0; i < 50; i++ {
		blocks := limiterB.GetActiveBlocks()
		if _, exists := blocks[testIP]; exists {
			blockedOnB = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !blockedOnB {
		t.Fatalf("Expected testIP to be blocked on Node B after Gossip sync, but it was not found in active blocks")
	}

	// Verify Node B returns false on Allow for the blocked IP
	if limiterB.Allow(testIP) {
		t.Errorf("Expected Allow to return false for blocked IP on Node B, but it returned true")
	}
}
