package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/ManiSaiTeja2007/aeroproxy/internal/encryption"
	"github.com/ManiSaiTeja2007/aeroproxy/internal/metrics"
)

var sensitiveKeys = map[string]bool{
	"email":       true,
	"ssn":         true,
	"credit_card": true,
}

// encryptMap scans the given map for sensitive keys and encrypts their string values.
func encryptMap(m map[string]interface{}, key []byte) {
	for k, v := range m {
		if sensitiveKeys[k] {
			if strVal, ok := v.(string); ok {
				if encrypted, err := encryption.Encrypt(strVal, key); err == nil {
					m[k] = encrypted
				}
			}
		}
	}
}

// DataShifterMiddleware intercepts POST/PUT requests, wrapping the body to limit OOM risks,
// scanning JSON payloads for sensitive keys, encrypting them, and forwarding the mutated body.
func DataShifterMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only intercept POST and PUT requests
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			next.ServeHTTP(w, r)
			return
		}

		// Keep a reference to the original body for fallback
		originalBody := r.Body

		// Limit the request body to 5MB to prevent OOM attacks
		r.Body = http.MaxBytesReader(w, r.Body, 5*1024*1024)

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			// Graceful Fallback: restore body and proceed
			r.Body = originalBody
			next.ServeHTTP(w, r)
			return
		}

		if len(bodyBytes) == 0 {
			// Restore empty body and proceed
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			next.ServeHTTP(w, r)
			return
		}

		var parsed interface{}
		if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
			// Graceful Fallback: not valid JSON, proceed as is
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			next.ServeHTTP(w, r)
			return
		}

		key := encryption.GetKey()
		mutated := false

		startTime := time.Now()
		switch data := parsed.(type) {
		case map[string]interface{}:
			// Single map -> process synchronously to avoid thread overhead
			encryptMap(data, key)
			mutated = true

		case []interface{}:
			// Array of elements -> process maps concurrently using goroutines
			// Safe: Each goroutine operates on a unique map pointer within the slice, preventing concurrent map access panics.
			var wg sync.WaitGroup
			for i := range data {
				item := data[i]
				if m, ok := item.(map[string]interface{}); ok {
					wg.Add(1)
					go func(mMap map[string]interface{}) {
						defer wg.Done()
						encryptMap(mMap, key)
					}(m)
				}
			}
			wg.Wait()
			mutated = true
		}

		if mutated {
			durationMs := float64(time.Since(startTime).Nanoseconds()) / 1e6
			metrics.EncryptionLatency.Observe(durationMs)

			newBody, err := json.Marshal(parsed)
			if err != nil {
				// Graceful Fallback: fail to marshal, proceed with original body
				r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
				next.ServeHTTP(w, r)
				return
			}

			r.Body = io.NopCloser(bytes.NewReader(newBody))
			r.ContentLength = int64(len(newBody))
			r.Header.Set("Content-Length", fmt.Sprintf("%d", len(newBody)))
		} else {
			// Body was not mutated, restore read bytes
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}

		next.ServeHTTP(w, r)
	})
}
