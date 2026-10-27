package api

import (
	"errors"
	"io"
	"net"
	"syscall"

	"golang.org/x/net/http2"
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
	if isHTTP2StreamError(err) {
		return true
	}
	return false
}

// isHTTP2StreamError reports whether err is a server-initiated HTTP/2 stream
// reset (CANCEL / REFUSED_STREAM / INTERNAL_ERROR) or GOAWAY, which SharePoint
// uses to shed load and is safe to retry on an idempotent request.
func isHTTP2StreamError(err error) bool {
	if err == nil {
		return false
	}
	var se http2.StreamError
	if errors.As(err, &se) {
		switch se.Code {
		case http2.ErrCodeCancel, http2.ErrCodeRefusedStream, http2.ErrCodeInternal:
			return true
		}
	}
	var ga http2.GoAwayError
	return errors.As(err, &ga)
}

// isIdempotent reports whether the HTTP method is safe to retry.
func isIdempotent(method string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return true
	}
	return false
}
