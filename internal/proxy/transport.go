package proxy

import (
	"net/http"
	"time"
)

// TrackingTransport wraps http.RoundTripper to record telemetry and circuit-breaking state.
type TrackingTransport struct {
	Backend   *Backend
	Transport http.RoundTripper
}

// RoundTrip executes a single HTTP transaction, updating backend metrics.
func (t *TrackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.Backend.IncActive()
	defer t.Backend.DecActive()

	start := time.Now()

	transport := t.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	res, err := transport.RoundTrip(r)

	durationMs := float64(time.Since(start).Nanoseconds()) / 1e6
	t.Backend.UpdateEWMA(durationMs)

	if err != nil || (res != nil && res.StatusCode >= 500) {
		t.Backend.RecordError()
	} else {
		t.Backend.RecordSuccess()
	}

	return res, err
}
