package processors

import (
	"context"
	"fmt"
	"io"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"

	"sprawler/internal/model"
)

func TestParseError_ConnectionReset(t *testing.T) {
	cat, status := parseError(syscall.ECONNRESET)
	assert.Equal(t, model.ErrNetwork, cat)
	assert.Equal(t, 0, status)
}

func TestParseError_WrappedConnectionReset(t *testing.T) {
	err := fmt.Errorf("fetch failed: %w", syscall.ECONNRESET)
	cat, status := parseError(err)
	assert.Equal(t, model.ErrNetwork, cat)
	assert.Equal(t, 0, status)
}

func TestParseError_HTTPStatusCodes(t *testing.T) {
	tests := []struct {
		name     string
		errMsg   string
		wantCat  model.ErrorCategory
		wantCode int
	}{
		{"429 throttle", "429 Too Many Requests", model.ErrThrottle, 429},
		{"401 auth", "401 Unauthorized", model.ErrAuth, 401},
		{"403 forbidden", "403 Forbidden", model.ErrAuth, 403},
		{"500 server", "500 Internal Server Error", model.ErrServerError, 500},
		{"503 unavailable", "503 Service Unavailable", model.ErrServerError, 503},
		{"404 not found", "404 Not Found", model.ErrUnknown, 404},
		{"short message", "hi", model.ErrUnknown, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat, code := parseError(fmt.Errorf("%s", tt.errMsg))
			assert.Equal(t, tt.wantCat, cat)
			assert.Equal(t, tt.wantCode, code)
		})
	}
}

func TestParseError_DeadlineExceeded(t *testing.T) {
	cat, status := parseError(context.DeadlineExceeded)
	assert.Equal(t, model.ErrTimeout, cat)
	assert.Equal(t, 0, status)
}

func TestParseError_WrappedDeadlineExceeded(t *testing.T) {
	err := fmt.Errorf("request failed: %w", context.DeadlineExceeded)
	cat, status := parseError(err)
	assert.Equal(t, model.ErrTimeout, cat)
	assert.Equal(t, 0, status)
}

func TestParseError_EOF(t *testing.T) {
	cat, status := parseError(io.EOF)
	assert.Equal(t, model.ErrNetwork, cat)
	assert.Equal(t, 0, status)
}

func TestParseError_UnexpectedEOF(t *testing.T) {
	cat, status := parseError(io.ErrUnexpectedEOF)
	assert.Equal(t, model.ErrNetwork, cat)
	assert.Equal(t, 0, status)
}
