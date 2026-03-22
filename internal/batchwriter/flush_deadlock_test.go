package batchwriter

import (
	"context"
	"testing"
	"time"
)

func TestFlushDoesNotBlockAfterClose(t *testing.T) {
	rec := newRecorder()
	ctx := context.Background()
	mw := New(ctx, 100, 0, 16)

	mw.Register("A", func(ctx context.Context, v any) error {
		rec.add("A", v, 0)
		return nil
	})
	mw.Add(Item{Kind: "A", Value: "test"})

	// Close the writer
	mw.Close()

	// This should not block forever - context should be cancelled
	done := make(chan struct{})
	go func() {
		mw.Flush() // Should return quickly due to cancelled context
		close(done)
	}()

	// Wait with timeout
	select {
	case <-done:
		// Success - Flush() returned
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Flush() blocked after Close() - deadlock detected")
	}
}
