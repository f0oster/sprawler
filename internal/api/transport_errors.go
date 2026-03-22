package api

import (
	"errors"
	"io"
	"net"
	"syscall"
)

// isConnectionReset reports whether err is a TCP connection reset.
func isConnectionReset(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.Errno(10054))
}

// isTransientNetError reports whether err is a transient network failure.
func isTransientNetError(err error) bool {
	if err == nil {
		return false
	}
	if isConnectionReset(err) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return false
}

// isIdempotent reports whether the HTTP method is safe to retry.
func isIdempotent(method string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return true
	}
	return false
}
