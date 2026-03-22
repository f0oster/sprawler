package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/koltyakov/gosip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sprawler/internal/logger"
)

// stubAuth implements gosip.AuthCnfg with no-op auth for testing.
type stubAuth struct {
	siteURL string
}

func (s *stubAuth) GetAuth() (string, int64, error) { return "Bearer test", 0, nil }
func (s *stubAuth) SetAuth(r *http.Request, _ *gosip.SPClient) error {
	r.Header.Set("Authorization", "Bearer test")
	return nil
}
func (s *stubAuth) ParseConfig([]byte) error { return nil }
func (s *stubAuth) ReadConfig(string) error  { return nil }
func (s *stubAuth) GetSiteURL() string       { return s.siteURL }
func (s *stubAuth) GetStrategy() string      { return "stub" }

// newGosipTestClient creates a gosip.SPClient pointed at a test server with our hooks installed.
func newGosipTestClient(serverURL string, retryPolicies map[int]int) *Client {
	auth := &stubAuth{siteURL: serverURL}
	gosipClient := &gosip.SPClient{AuthCnfg: auth}
	if retryPolicies != nil {
		gosipClient.RetryPolicies = retryPolicies
	}

	c := &Client{
		gosipClient: gosipClient,
		logger:      logger.NewLogger("test"),
		metrics:     &atomicMetrics{statusCodes: newCounterMap()},
	}
	c.setupHooks()
	return c
}

// statusSequenceHandler returns an http.Handler that serves the given status codes
// in order, cycling back to the last one if more requests arrive.
func statusSequenceHandler(codes ...int) http.Handler {
	var idx atomic.Int64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(idx.Add(1) - 1)
		code := codes[len(codes)-1] // default to last
		if i < len(codes) {
			code = codes[i]
		}
		w.WriteHeader(code)
	})
}

// statusCodeSum returns the total count across all entries in the status code map.
func statusCodeSum(codes map[int]int64) int64 {
	var sum int64
	for _, v := range codes {
		sum += v
	}
	return sum
}

// These tests verify metrics observability by exercising real gosip Execute calls
// against test HTTP servers. They assert that every HTTP response is visible
// in statusCodes and that statusCodes never double-counts responses that go
// through both onError and onResponse.

// TestMetricsVisibility_503ThenSuccess verifies that a 503 retried to success
// appears in statusCodes.
func TestMetricsVisibility_503ThenSuccess(t *testing.T) {
	srv := httptest.NewServer(statusSequenceHandler(503, 200))
	defer srv.Close()

	c := newGosipTestClient(srv.URL, map[int]int{503: 3})

	req, _ := http.NewRequest("GET", srv.URL+"/_api/web", nil)
	resp, err := c.gosipClient.Execute(req)
	require.NoError(t, err)
	resp.Body.Close()

	m := c.GetMetrics()

	assert.Equal(t, int64(2), statusCodeSum(m.StatusCodes), "2 attempts total")
	assert.Equal(t, int64(1), m.StatusCodes[503], "503 should appear in status codes")
	assert.Equal(t, int64(1), m.StatusCodes[200])
}

// TestMetricsVisibility_503x2ThenSuccess verifies multiple intermediate 503s
// are each recorded once.
func TestMetricsVisibility_503x2ThenSuccess(t *testing.T) {
	srv := httptest.NewServer(statusSequenceHandler(503, 503, 200))
	defer srv.Close()

	c := newGosipTestClient(srv.URL, map[int]int{503: 5})

	req, _ := http.NewRequest("GET", srv.URL+"/_api/web", nil)
	resp, err := c.gosipClient.Execute(req)
	require.NoError(t, err)
	resp.Body.Close()

	m := c.GetMetrics()

	assert.Equal(t, int64(3), statusCodeSum(m.StatusCodes), "3 attempts total")
	assert.Equal(t, int64(2), m.StatusCodes[503], "both 503s should appear")
	assert.Equal(t, int64(1), m.StatusCodes[200])
}

// TestMetricsVisibility_503Exhausted verifies exhausted 503 retries. The final
// 503 goes through both onError and onResponse but should be counted once in
// statusCodes.
func TestMetricsVisibility_503Exhausted(t *testing.T) {
	srv := httptest.NewServer(statusSequenceHandler(503)) // always 503
	defer srv.Close()

	c := newGosipTestClient(srv.URL, map[int]int{503: 2})

	req, _ := http.NewRequest("GET", srv.URL+"/_api/web", nil)
	resp, err := c.gosipClient.Execute(req)
	require.Error(t, err)
	resp.Body.Close()

	m := c.GetMetrics()

	assert.Equal(t, int64(3), statusCodeSum(m.StatusCodes), "3 attempts: initial + 2 retries")
	assert.Equal(t, int64(3), m.StatusCodes[503],
		"three 503 responses should produce statusCodes[503]=3")
}

// TestMetricsVisibility_429ThenSuccess verifies 429->200 statusCodes completeness.
func TestMetricsVisibility_429ThenSuccess(t *testing.T) {
	srv := httptest.NewServer(statusSequenceHandler(429, 200))
	defer srv.Close()

	c := newGosipTestClient(srv.URL, map[int]int{429: 3})

	req, _ := http.NewRequest("GET", srv.URL+"/_api/web", nil)
	resp, err := c.gosipClient.Execute(req)
	require.NoError(t, err)
	resp.Body.Close()

	m := c.GetMetrics()

	assert.Equal(t, int64(1), m.StatusCodes[429], "429 should appear once")
	assert.Equal(t, int64(1), m.StatusCodes[200])
}

// TestMetricsVisibility_429Exhausted verifies exhausted 429 retries. The final
// attempt fires onError(429) + onResponse(429) -- statusCodes should count each
// HTTP response once, not once per hook call.
func TestMetricsVisibility_429Exhausted(t *testing.T) {
	srv := httptest.NewServer(statusSequenceHandler(429)) // always 429
	defer srv.Close()

	c := newGosipTestClient(srv.URL, map[int]int{429: 1})

	req, _ := http.NewRequest("GET", srv.URL+"/_api/web", nil)
	resp, err := c.gosipClient.Execute(req)
	require.Error(t, err)
	resp.Body.Close()

	m := c.GetMetrics()

	assert.Equal(t, int64(2), statusCodeSum(m.StatusCodes), "2 attempts: initial + 1 retry")
	assert.Equal(t, int64(2), m.StatusCodes[429],
		"two 429 responses should produce statusCodes[429]=2, not 3")
}

// TestMetricsVisibility_404NoDoubleCount verifies non-retried errors are not
// double-counted in statusCodes. gosip fires both onError(404) and onResponse(404)
// for the same response.
func TestMetricsVisibility_404NoDoubleCount(t *testing.T) {
	srv := httptest.NewServer(statusSequenceHandler(404))
	defer srv.Close()

	c := newGosipTestClient(srv.URL, nil)

	req, _ := http.NewRequest("GET", srv.URL+"/_api/web", nil)
	resp, err := c.gosipClient.Execute(req)
	require.Error(t, err)
	resp.Body.Close()

	m := c.GetMetrics()

	assert.Equal(t, int64(1), m.StatusCodes[404],
		"404 should appear once, not twice (onError + onResponse both fire)")
	assert.Equal(t, int64(1), statusCodeSum(m.StatusCodes))
}

// TestMetricsVisibility_200Simple verifies the happy path is unaffected.
func TestMetricsVisibility_200Simple(t *testing.T) {
	srv := httptest.NewServer(statusSequenceHandler(200))
	defer srv.Close()

	c := newGosipTestClient(srv.URL, nil)

	req, _ := http.NewRequest("GET", srv.URL+"/_api/web", nil)
	resp, err := c.gosipClient.Execute(req)
	require.NoError(t, err)
	resp.Body.Close()

	m := c.GetMetrics()

	assert.Equal(t, int64(1), m.StatusCodes[200])
	assert.Equal(t, int64(1), statusCodeSum(m.StatusCodes))
}

// TestMetricsVisibility_401 verifies that 401 status codes appear correctly
// in the status code map with retries.
func TestMetricsVisibility_401(t *testing.T) {
	srv := httptest.NewServer(statusSequenceHandler(401))
	defer srv.Close()

	c := newGosipTestClient(srv.URL, map[int]int{401: 1})

	req, _ := http.NewRequest("GET", srv.URL+"/_api/web", nil)
	resp, err := c.gosipClient.Execute(req)
	require.Error(t, err)
	resp.Body.Close()

	m := c.GetMetrics()

	assert.Equal(t, int64(2), m.StatusCodes[401], "initial + 1 retry")
	assert.Equal(t, int64(2), statusCodeSum(m.StatusCodes))
}

// --- Network error tests ---
//
// These use a custom RoundTripper injected into the real gosip client to control
// what errors the transport returns. Gosip's Execute method, our hooks, and all
// metrics run for real -- only the network layer is a test double.

// responseSequenceTransport returns responses and errors from a predetermined
// sequence. Each call pops the next entry; after exhaustion it repeats the last.
type responseSequenceTransport struct {
	entries []transportResponse
	idx     atomic.Int64
}

type transportResponse struct {
	statusCode int // 0 means return err instead
	err        error
}

func (t *responseSequenceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	i := int(t.idx.Add(1) - 1)
	entry := t.entries[len(t.entries)-1]
	if i < len(t.entries) {
		entry = t.entries[i]
	}
	if entry.err != nil {
		return nil, entry.err
	}
	return &http.Response{
		StatusCode: entry.statusCode,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

// newGosipTestClientWithTransport creates a gosip client that uses the given
// RoundTripper instead of making real HTTP calls.
func newGosipTestClientWithTransport(transport http.RoundTripper) *Client {
	auth := &stubAuth{siteURL: "https://test.sharepoint.com"}
	gosipClient := &gosip.SPClient{AuthCnfg: auth}
	gosipClient.Transport = transport

	c := &Client{
		gosipClient: gosipClient,
		logger:      logger.NewLogger("test"),
		metrics:     &atomicMetrics{statusCodes: newCounterMap()},
	}
	c.setupHooks()
	return c
}

// TestMetricsVisibility_ConnectionReset verifies that ECONNRESET is classified
// as a network error. Network errors have no HTTP status code, so statusCodes
// remains empty.
func TestMetricsVisibility_ConnectionReset(t *testing.T) {
	transport := &responseSequenceTransport{
		entries: []transportResponse{{err: syscall.ECONNRESET}},
	}
	c := newGosipTestClientWithTransport(transport)

	req, _ := http.NewRequest("GET", "https://test.sharepoint.com/_api/web", nil)
	_, err := c.gosipClient.Execute(req)
	require.Error(t, err)

	m := c.GetMetrics()

	assert.Equal(t, int64(1), c.GetNetworkErrorCount(), "ECONNRESET should be classified as network error")
	assert.Equal(t, int64(0), statusCodeSum(m.StatusCodes),
		"network errors produce no HTTP response, so no status codes")
}

// TestMetricsVisibility_EOF verifies that EOF is classified as a network error.
func TestMetricsVisibility_EOF(t *testing.T) {
	transport := &responseSequenceTransport{
		entries: []transportResponse{{err: io.EOF}},
	}
	c := newGosipTestClientWithTransport(transport)

	req, _ := http.NewRequest("GET", "https://test.sharepoint.com/_api/web", nil)
	_, err := c.gosipClient.Execute(req)
	require.Error(t, err)

	m := c.GetMetrics()

	assert.Equal(t, int64(1), c.GetNetworkErrorCount(), "EOF should be classified as network error")
	assert.Equal(t, int64(0), statusCodeSum(m.StatusCodes))
}

// TestMetricsVisibility_503ThenConnectionReset verifies the scenario where gosip
// retries a 503 but the retry attempt hits a connection reset. The 503 should be
// visible in statusCodes, and the ECONNRESET should increment networkErrors.
func TestMetricsVisibility_503ThenConnectionReset(t *testing.T) {
	transport := &responseSequenceTransport{
		entries: []transportResponse{
			{statusCode: 503},
			{err: syscall.ECONNRESET},
		},
	}
	c := newGosipTestClientWithTransport(transport)
	c.gosipClient.RetryPolicies = map[int]int{503: 3}

	req, _ := http.NewRequest("GET", "https://test.sharepoint.com/_api/web", nil)
	_, err := c.gosipClient.Execute(req)
	require.Error(t, err)

	m := c.GetMetrics()

	assert.Equal(t, int64(1), statusCodeSum(m.StatusCodes), "only 503 has a status code")
	assert.Equal(t, int64(1), c.GetNetworkErrorCount(), "ECONNRESET classified as network error")
	assert.Equal(t, int64(1), m.StatusCodes[503], "503 should be visible")
	assert.Equal(t, int64(1), c.GetThrottlingCount(), "503 should count as throttling")
}
