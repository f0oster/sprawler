package processors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sprawler/internal/api"
	"sprawler/internal/logger"
	"sprawler/internal/model"
)

// formatCompact formats a number compactly: exact below 10k, "Xk" for 10k+, "X.YM" for 1M+.
func formatCompact(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// formatDuration formats a duration as a concise human-readable string.
func formatDuration(d time.Duration) string {
	if d >= time.Hour {
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	if d >= time.Minute {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// parseError extracts error category and HTTP status from a gosip error.
// gosip format: "429 Too Many Requests :: <body>", wrapped by API client.
func parseError(err error) (model.ErrorCategory, int) {
	if errors.Is(err, context.DeadlineExceeded) {
		return model.ErrTimeout, 0
	}
	// Unwrap to root error and parse leading HTTP status
	inner := err
	for {
		if u := errors.Unwrap(inner); u != nil {
			inner = u
		} else {
			break
		}
	}
	msg := inner.Error()
	if len(msg) >= 3 {
		if code, e := strconv.Atoi(msg[:3]); e == nil && code >= 400 {
			switch {
			case code == 429:
				return model.ErrThrottle, code
			case code == 401 || code == 403:
				return model.ErrAuth, code
			case code >= 500:
				// Note: 503 is counted as throttling in api.Client.onError metrics,
				// but classified as server_error here for outcome recording. By the
				// time parseError sees a 503, gosip has already exhausted its retries.
				return model.ErrServerError, code
			default:
				return model.ErrUnknown, code
			}
		}
	}
	// Network-level errors
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.Errno(10054)) {
		return model.ErrNetwork, 0
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return model.ErrNetwork, 0
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return model.ErrNetwork, 0
	}
	return model.ErrUnknown, 0
}

// newOperationFailure creates an OperationFailure from an error.
func newOperationFailure(err error) *model.OperationFailure {
	cat, status := parseError(err)
	return &model.OperationFailure{Category: cat, HTTPStatus: status, Detail: err.Error()}
}

// logAPIDiagnostics logs API latency, status code breakdown, and transport retries.
func logAPIDiagnostics(logger *logger.Logger, apiMetrics model.APIMetrics) {
	var reqCount int64
	var codes []int
	for code, count := range apiMetrics.StatusCodes {
		codes = append(codes, code)
		reqCount += count
	}

	sort.Ints(codes)
	var statusParts []string
	for _, code := range codes {
		statusParts = append(statusParts, fmt.Sprintf("%d:%d", code, apiMetrics.StatusCodes[code]))
	}

	line := fmt.Sprintf("API: %d req, %dms avg", reqCount, apiMetrics.AvgDuration)
	if len(statusParts) > 0 {
		line += fmt.Sprintf(" [%s]", strings.Join(statusParts, " "))
	}
	if apiMetrics.TransportRetries > 0 {
		line += fmt.Sprintf(", %d transport retries", apiMetrics.TransportRetries)
	}

	logger.Infof("%s", line)
}

// logTransportDiagnostics logs transport-level stats when any activity occurred.
func logTransportDiagnostics(logger *logger.Logger, stats api.TransportStats) {
	if stats.GateActivations > 0 || stats.TransportRetries > 0 {
		logger.Infof("Transport - Gate activations: %d, Total gate wait: %s, Transport retries: %d",
			stats.GateActivations, stats.TotalGateWait.Truncate(time.Millisecond), stats.TransportRetries)
	}
}

// formatFailureBreakdown returns sorted "N category" strings from OutcomeCollector.FailureSummary().
func formatFailureBreakdown(oc *OutcomeCollector) []string {
	failSummary := oc.FailureSummary()
	var failKeys []string
	for k := range failSummary {
		failKeys = append(failKeys, k)
	}
	sort.Strings(failKeys)
	var parts []string
	for _, k := range failKeys {
		parts = append(parts, fmt.Sprintf("%d %s", failSummary[k], k))
	}
	return parts
}
