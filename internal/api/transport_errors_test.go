package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsConnectionReset(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"ECONNRESET", syscall.ECONNRESET, true},
		{"Windows WSAECONNRESET", syscall.Errno(10054), true},
		{"wrapped ECONNRESET", fmt.Errorf("wrapper: %w", syscall.ECONNRESET), true},
		{"io.EOF", io.EOF, false},
		{"generic error", errors.New("some other error"), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isConnectionReset(tt.err))
		})
	}
}

func TestIsTransientNetError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"ECONNRESET", syscall.ECONNRESET, true},
		{"io.EOF", io.EOF, true},
		{"io.ErrUnexpectedEOF", io.ErrUnexpectedEOF, true},
		{"net.OpError wrapping syscall", &net.OpError{
			Op:  "read",
			Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET},
		}, true},
		{"generic error", errors.New("permission denied"), false},
		{"context.Canceled", context.Canceled, false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTransientNetError(tt.err))
		})
	}
}

func TestIsIdempotent(t *testing.T) {
	tests := []struct {
		method string
		want   bool
	}{
		{"GET", true},
		{"HEAD", true},
		{"OPTIONS", true},
		{"POST", false},
		{"PUT", false},
		{"PATCH", false},
		{"DELETE", false},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			assert.Equal(t, tt.want, isIdempotent(tt.method))
		})
	}
}
