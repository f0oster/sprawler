package api

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sprawler/internal/logger"
)

// mockRoundTripper calls fn on each RoundTrip invocation.
type mockRoundTripper struct {
	fn    func(req *http.Request) (*http.Response, error)
	calls atomic.Int32
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.calls.Add(1)
	return m.fn(req)
}

func newTestTransport(base http.RoundTripper) *ThrottledTransport {
	return &ThrottledTransport{
		base:          base,
		logger:        logger.NewLogger("test"),
		maxRetries:    3,
		retryBackoffs: []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second},
		throttlePause: 1 * time.Minute,
	}
}

func TestThrottledTransport_RetriesConnectionReset(t *testing.T) {
	var attempt atomic.Int32
	mock := &mockRoundTripper{fn: func(req *http.Request) (*http.Response, error) {
		n := attempt.Add(1)
		if n <= 2 {
			return nil, syscall.ECONNRESET
		}
		return &http.Response{StatusCode: 200}, nil
	}}

	transport := newTestTransport(mock)
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	resp, err := transport.RoundTrip(req)

	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, int32(3), mock.calls.Load())
}

func TestThrottledTransport_NoRetryOnNonTransientError(t *testing.T) {
	mock := &mockRoundTripper{fn: func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dns lookup failed")
	}}

	transport := newTestTransport(mock)
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	_, err := transport.RoundTrip(req)

	assert.Error(t, err)
	assert.Equal(t, int32(1), mock.calls.Load())
}

func TestThrottledTransport_NoRetryForPOST(t *testing.T) {
	mock := &mockRoundTripper{fn: func(req *http.Request) (*http.Response, error) {
		return nil, syscall.ECONNRESET
	}}

	transport := newTestTransport(mock)
	req, _ := http.NewRequest("POST", "https://example.com", nil)
	_, err := transport.RoundTrip(req)

	assert.Error(t, err)
	assert.Equal(t, int32(1), mock.calls.Load())
}

func TestThrottledTransport_RespectsContextCancellation(t *testing.T) {
	mock := &mockRoundTripper{fn: func(req *http.Request) (*http.Response, error) {
		return nil, syscall.ECONNRESET
	}}

	transport := newTestTransport(mock)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://example.com", nil)
	_, err := transport.RoundTrip(req)

	assert.ErrorIs(t, err, context.Canceled)
	// The first RoundTrip call happens before context check, so 1 call
	assert.Equal(t, int32(1), mock.calls.Load())
}

func TestThrottledTransport_ExhaustsRetries(t *testing.T) {
	mock := &mockRoundTripper{fn: func(req *http.Request) (*http.Response, error) {
		return nil, syscall.ECONNRESET
	}}

	transport := newTestTransport(mock)
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	_, err := transport.RoundTrip(req)

	assert.Error(t, err)
	// 1 initial + 3 retries = 4 total
	assert.Equal(t, int32(4), mock.calls.Load())
}

// getPauseUntil reads pauseUntil under the mutex for test assertions.
func getPauseUntil(t *ThrottledTransport) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pauseUntil
}

func TestThrottledTransport_429WithRetryAfter(t *testing.T) {
	mock := &mockRoundTripper{fn: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 429,
			Header:     http.Header{"Retry-After": []string{"5"}},
		}, nil
	}}

	transport := newTestTransport(mock)
	before := time.Now()
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	resp, err := transport.RoundTrip(req)

	require.NoError(t, err)
	assert.Equal(t, 429, resp.StatusCode)

	pause := getPauseUntil(transport)
	expected := before.Add(5 * time.Second)
	assert.WithinDuration(t, expected, pause, 1*time.Second)
}

func TestThrottledTransport_503WithRetryAfter(t *testing.T) {
	mock := &mockRoundTripper{fn: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 503,
			Header:     http.Header{"Retry-After": []string{"10"}},
		}, nil
	}}

	transport := newTestTransport(mock)
	before := time.Now()
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	resp, err := transport.RoundTrip(req)

	require.NoError(t, err)
	assert.Equal(t, 503, resp.StatusCode)

	pause := getPauseUntil(transport)
	expected := before.Add(10 * time.Second)
	assert.WithinDuration(t, expected, pause, 1*time.Second)
}

func TestThrottledTransport_429WithoutRetryAfter(t *testing.T) {
	mock := &mockRoundTripper{fn: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 429,
			Header:     http.Header{},
		}, nil
	}}

	transport := newTestTransport(mock)
	before := time.Now()
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	resp, err := transport.RoundTrip(req)

	require.NoError(t, err)
	assert.Equal(t, 429, resp.StatusCode)

	pause := getPauseUntil(transport)
	expected := before.Add(transport.throttlePause)
	assert.WithinDuration(t, expected, pause, 1*time.Second)
}

func TestThrottledTransport_503WithoutRetryAfter(t *testing.T) {
	mock := &mockRoundTripper{fn: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 503,
			Header:     http.Header{},
		}, nil
	}}

	transport := newTestTransport(mock)
	before := time.Now()
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	resp, err := transport.RoundTrip(req)

	require.NoError(t, err)
	assert.Equal(t, 503, resp.StatusCode)

	pause := getPauseUntil(transport)
	expected := before.Add(transport.throttlePause)
	assert.WithinDuration(t, expected, pause, 1*time.Second)
}

func TestThrottledTransport_GateBlocksSubsequentRequests(t *testing.T) {
	mock := &mockRoundTripper{fn: func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}}, nil
	}}

	transport := newTestTransport(mock)
	gateDuration := 150 * time.Millisecond
	transport.mu.Lock()
	transport.pauseUntil = time.Now().Add(gateDuration)
	transport.mu.Unlock()

	start := time.Now()
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	resp, err := transport.RoundTrip(req)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.GreaterOrEqual(t, elapsed, gateDuration-10*time.Millisecond,
		"request should have waited for the gate to open")
}

func TestThrottledTransport_GateRespectsContextCancellation(t *testing.T) {
	mock := &mockRoundTripper{fn: func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}}, nil
	}}

	transport := newTestTransport(mock)
	transport.mu.Lock()
	transport.pauseUntil = time.Now().Add(10 * time.Second)
	transport.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://example.com", nil)
	_, err := transport.RoundTrip(req)

	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int32(0), mock.calls.Load(), "base transport should not be called")
}

func TestThrottledTransport_GateDoesNotRegress(t *testing.T) {
	// The mock simulates a concurrent goroutine setting a longer pause:
	// during the RoundTrip call, before the 429 handler runs, another
	// goroutine has already pushed pauseUntil to now+30s. The 429's
	// Retry-After: 5 should NOT overwrite that.
	var transport *ThrottledTransport
	longPause := time.Now().Add(30 * time.Second)

	mock := &mockRoundTripper{fn: func(req *http.Request) (*http.Response, error) {
		// Simulate another goroutine having set a longer pause while this
		// request was in flight.
		transport.mu.Lock()
		transport.pauseUntil = longPause
		transport.mu.Unlock()

		return &http.Response{
			StatusCode: 429,
			Header:     http.Header{"Retry-After": []string{"5"}},
		}, nil
	}}

	transport = newTestTransport(mock)
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	_, _ = transport.RoundTrip(req)

	pause := getPauseUntil(transport)
	assert.True(t, !pause.Before(longPause),
		"pauseUntil should not regress: got %v, want >= %v", pause, longPause)
}
