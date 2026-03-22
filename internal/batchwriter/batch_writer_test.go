package batchwriter

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// call captures one op invocation.
type call struct {
	kind string
	val  any
	dur  time.Duration
}

// recorder accumulates calls in a threadsafe way and can signal when N calls observed.
type recorder struct {
	mu     sync.Mutex
	calls  []call
	notify chan struct{}
}

func newRecorder() *recorder {
	return &recorder{notify: make(chan struct{}, 1)}
}

func (r *recorder) add(kind string, val any, dur time.Duration) {
	r.mu.Lock()
	r.calls = append(r.calls, call{kind: kind, val: val, dur: dur})
	r.mu.Unlock()
	// non-blocking notify
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *recorder) snapshot() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]call, len(r.calls))
	copy(out, r.calls)
	return out
}

func waitForCalls(t *testing.T, r *recorder, want int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		if len(r.snapshot()) >= want {
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timeout waiting for %d calls; got %d", want, len(r.snapshot()))
		}
		select {
		case <-r.notify:
		case <-time.After(remaining / 10):
		}
	}
}

// --- Tests ---

func TestFIFOOrder(t *testing.T) {
	rec := newRecorder()
	ctx := context.Background()
	mw := New(ctx, 100, 0, 16)
	defer mw.Close()

	mw.Register("A", func(ctx context.Context, v any) error {
		rec.add("A", v, 0)
		return nil
	})
	mw.Register("B", func(ctx context.Context, v any) error {
		rec.add("B", v, 0)
		return nil
	})

	// Enqueue: A,A,B,B,A => expect 5 individual calls in exact FIFO order
	mw.Add(Item{Kind: "A", Value: 1})
	mw.Add(Item{Kind: "A", Value: 2})
	mw.Add(Item{Kind: "B", Value: "x"})
	mw.Add(Item{Kind: "B", Value: "y"})
	mw.Add(Item{Kind: "A", Value: 3})

	mw.Flush()
	waitForCalls(t, rec, 5, 2*time.Second)

	calls := rec.snapshot()
	if len(calls) != 5 {
		t.Fatalf("expected 5 calls, got %d", len(calls))
	}
	assertCall(t, calls[0], "A", 1)
	assertCall(t, calls[1], "A", 2)
	assertCall(t, calls[2], "B", "x")
	assertCall(t, calls[3], "B", "y")
	assertCall(t, calls[4], "A", 3)

	// Stats: one flush = one transaction
	ok, fail, tx := mw.GetStats()
	if ok != 5 || fail != 0 || tx != 1 {
		t.Fatalf("stats mismatch: ok=%d fail=%d tx=%d (want 5,0,1)", ok, fail, tx)
	}
}

func TestSizeBasedFlush(t *testing.T) {
	rec := newRecorder()
	ctx := context.Background()
	mw := New(ctx, 3, 0, 16) // flush when 3 buffered
	defer mw.Close()

	mw.Register("A", func(ctx context.Context, v any) error {
		rec.add("A", v, 0)
		return nil
	})

	// Push 3 -> triggers eager flush; then 1 more -> will remain buffered until final Close
	for _, v := range []int{10, 11, 12, 13} {
		mw.Add(Item{Kind: "A", Value: v})
	}

	// Wait for first batch (3 items) to flush
	waitForCalls(t, rec, 3, 2*time.Second)
	first := rec.snapshot()
	assertCall(t, first[0], "A", 10)
	assertCall(t, first[1], "A", 11)
	assertCall(t, first[2], "A", 12)

	// Close should drain the last item
	mw.Close()
	calls := rec.snapshot()
	if len(calls) != 4 {
		t.Fatalf("expected 4 calls, got %d", len(calls))
	}
	assertCall(t, calls[3], "A", 13)
}

func TestTimerBasedFlush(t *testing.T) {
	rec := newRecorder()
	ctx := context.Background()
	mw := New(ctx, 100, 20*time.Millisecond, 16) // periodic flush
	defer mw.Close()

	mw.Register("A", func(ctx context.Context, v any) error {
		rec.add("A", v, 0)
		return nil
	})

	mw.Add(Item{Kind: "A", Value: "t0"})
	// No explicit Flush(); rely on ticker
	waitForCalls(t, rec, 1, time.Second)
	assertCall(t, rec.snapshot()[0], "A", "t0")
}

func TestFlushIsSynchronous(t *testing.T) {
	rec := newRecorder()
	ctx := context.Background()
	mw := New(ctx, 100, 0, 16)
	defer mw.Close()

	mw.Register("A", func(ctx context.Context, v any) error {
		rec.add("A", v, 0)
		return nil
	})

	mw.Add(Item{Kind: "A", Value: 1})
	mw.Add(Item{Kind: "A", Value: 2})
	// After Flush() returns, both items should be recorded
	mw.Flush()

	if len(rec.snapshot()) != 2 {
		t.Fatalf("Flush() did not synchronously apply; calls=%d", len(rec.snapshot()))
	}
	assertCall(t, rec.snapshot()[0], "A", 1)
	assertCall(t, rec.snapshot()[1], "A", 2)
}

func TestCloseDrainsChannel(t *testing.T) {
	rec := newRecorder()
	ctx := context.Background()
	mw := New(ctx, 1000, 0, 1000) // large buffer, no timer
	defer mw.Close()

	mw.Register("A", func(ctx context.Context, v any) error {
		rec.add("A", v, 0)
		return nil
	})

	// Enqueue a bunch quickly, then Close immediately
	const N = 250
	for i := 0; i < N; i++ {
		mw.Add(Item{Kind: "A", Value: i})
	}
	mw.Close() // should drain all N

	total := len(rec.snapshot())
	if total != N {
		t.Fatalf("Close() did not drain all items; got %d, want %d", total, N)
	}
}

func TestUnregisteredKindCountsFailure(t *testing.T) {
	ctx := context.Background()
	mw := New(ctx, 100, 0, 16)
	defer mw.Close()

	// No registration for kind "X"
	mw.Add(Item{Kind: "X", Value: 1})
	mw.Flush()

	ok, fail, flushes := mw.GetStats()
	if ok != 0 || fail != 1 || flushes != 1 {
		t.Fatalf("stats mismatch for unregistered kind: ok=%d fail=%d flushes=%d (want 0,1,1)", ok, fail, flushes)
	}
}

func TestRegisterTypedAndEnqueue(t *testing.T) {
	rec := newRecorder()
	ctx := context.Background()
	mw := New(ctx, 100, 0, 16)
	defer mw.Close()

	type payload int
	kindP := NewKind[payload]("P")
	RegisterTyped(mw, kindP, func(ctx context.Context, v payload) error {
		rec.add("P", v, 0)
		return nil
	})

	Enqueue(mw, kindP, payload(7))
	Enqueue(mw, kindP, payload(8))
	mw.Flush()

	calls := rec.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected 2 typed calls, got %d", len(calls))
	}
	assertCall(t, calls[0], "P", payload(7))
	assertCall(t, calls[1], "P", payload(8))
}

func TestSingleExecution(t *testing.T) {
	ctx := context.Background()
	mw := New(ctx, 100, 0, 16)
	defer mw.Close()

	var calls int
	mw.Register("A", func(ctx context.Context, v any) error {
		calls++
		return nil
	})

	mw.Add(Item{Kind: "A", Value: "ok"})
	mw.Flush()

	if calls != 1 {
		t.Fatalf("expected 1 execution, got %d", calls)
	}
	ok, fail, tx := mw.GetStats()
	if ok != 1 || fail != 0 || tx != 1 {
		t.Fatalf("stats mismatch: ok=%d fail=%d tx=%d (want 1,0,1)", ok, fail, tx)
	}
}

func TestErrorCountsAllItemsAsFailed(t *testing.T) {
	ctx := context.Background()
	mw := New(ctx, 100, 0, 16)
	defer mw.Close()

	mw.Register("A", func(ctx context.Context, v any) error {
		return fmt.Errorf("tx failed")
	})

	mw.Add(Item{Kind: "A", Value: 1})
	mw.Add(Item{Kind: "A", Value: 2})
	mw.Add(Item{Kind: "A", Value: 3})
	mw.Flush()

	ok, fail, tx := mw.GetStats()
	if ok != 0 {
		t.Fatalf("expected 0 successful, got %d", ok)
	}
	if fail != 3 {
		t.Fatalf("expected 3 failed, got %d", fail)
	}
	if tx != 1 {
		t.Fatalf("expected 1 transaction, got %d", tx)
	}
}

// --- FlushHooks tests ---

func TestFlushHooks_BeforeAfterCalled(t *testing.T) {
	ctx := context.Background()
	mw := New(ctx, 100, 0, 16)
	defer mw.Close()

	var order []string
	mw.SetFlushHooks(&FlushHooks{
		Before: func(ctx context.Context) (context.Context, error) {
			order = append(order, "before")
			return ctx, nil
		},
		After: func(ctx context.Context, err error) error {
			order = append(order, "after")
			return err
		},
	})

	mw.Register("A", func(ctx context.Context, v any) error {
		order = append(order, "op-A")
		return nil
	})
	mw.Register("B", func(ctx context.Context, v any) error {
		order = append(order, "op-B")
		return nil
	})

	mw.Add(Item{Kind: "A", Value: 1})
	mw.Add(Item{Kind: "B", Value: 2})
	mw.Add(Item{Kind: "A", Value: 3})
	mw.Flush()

	// 5 individual op calls: A, B, A (each dispatched separately)
	want := []string{"before", "op-A", "op-B", "op-A", "after"}
	if len(order) != len(want) {
		t.Fatalf("call order length = %d, want %d: %v", len(order), len(want), order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("call order[%d] = %q, want %q; full: %v", i, order[i], want[i], order)
		}
	}
}

func TestFlushHooks_BeforeError(t *testing.T) {
	ctx := context.Background()
	mw := New(ctx, 100, 0, 16)
	defer mw.Close()

	var opCalled bool
	mw.SetFlushHooks(&FlushHooks{
		Before: func(ctx context.Context) (context.Context, error) {
			return ctx, fmt.Errorf("before failed")
		},
		After: func(ctx context.Context, err error) error {
			t.Fatal("After should not be called when Before fails")
			return err
		},
	})

	mw.Register("A", func(ctx context.Context, v any) error {
		opCalled = true
		return nil
	})

	mw.Add(Item{Kind: "A", Value: 1})
	mw.Add(Item{Kind: "A", Value: 2})
	mw.Flush()

	if opCalled {
		t.Fatal("op should not be called when Before hook fails")
	}
	ok, fail, tx := mw.GetStats()
	if ok != 0 || fail != 2 || tx != 1 {
		t.Fatalf("stats mismatch: ok=%d fail=%d tx=%d (want 0,2,1)", ok, fail, tx)
	}
}

func TestFlushHooks_OpErrorBreaks(t *testing.T) {
	ctx := context.Background()
	mw := New(ctx, 100, 0, 16)
	defer mw.Close()

	var afterErr error
	mw.SetFlushHooks(&FlushHooks{
		Before: func(ctx context.Context) (context.Context, error) {
			return ctx, nil
		},
		After: func(ctx context.Context, err error) error {
			afterErr = err
			return err
		},
	})

	var callCount int
	mw.Register("A", func(ctx context.Context, v any) error {
		callCount++
		return nil
	})
	mw.Register("B", func(ctx context.Context, v any) error {
		callCount++
		return fmt.Errorf("B failed")
	})

	mw.Add(Item{Kind: "A", Value: 1})
	mw.Add(Item{Kind: "B", Value: 2})
	mw.Add(Item{Kind: "A", Value: 3}) // should not execute
	mw.Flush()

	if callCount != 2 {
		t.Fatalf("expected 2 op calls (A then B), got %d", callCount)
	}
	if afterErr == nil || afterErr.Error() != "B failed" {
		t.Fatalf("After should receive the op error, got %v", afterErr)
	}
	ok, fail, _ := mw.GetStats()
	if ok != 0 || fail != 3 {
		t.Fatalf("all 3 items should be failed: ok=%d fail=%d", ok, fail)
	}
}

func TestFlushHooks_ContextThreading(t *testing.T) {
	type key struct{}
	ctx := context.Background()
	mw := New(ctx, 100, 0, 16)
	defer mw.Close()

	mw.SetFlushHooks(&FlushHooks{
		Before: func(ctx context.Context) (context.Context, error) {
			return context.WithValue(ctx, key{}, "hello"), nil
		},
	})

	var got any
	mw.Register("A", func(ctx context.Context, v any) error {
		got = ctx.Value(key{})
		return nil
	})

	mw.Add(Item{Kind: "A", Value: 1})
	mw.Flush()

	if got != "hello" {
		t.Fatalf("op did not receive context value from Before hook: got %v", got)
	}
}

func TestNoHooks_BackwardsCompatible(t *testing.T) {
	rec := newRecorder()
	ctx := context.Background()
	mw := New(ctx, 100, 0, 16)
	defer mw.Close()
	// No SetFlushHooks call

	mw.Register("A", func(ctx context.Context, v any) error {
		rec.add("A", v, 0)
		return nil
	})

	mw.Add(Item{Kind: "A", Value: 1})
	mw.Add(Item{Kind: "A", Value: 2})
	mw.Flush()

	ok, fail, tx := mw.GetStats()
	if ok != 2 || fail != 0 || tx != 1 {
		t.Fatalf("stats mismatch: ok=%d fail=%d tx=%d (want 2,0,1)", ok, fail, tx)
	}
}

// --- helpers ---

func assertCall(t *testing.T, c call, wantKind string, wantVal any) {
	t.Helper()
	if c.kind != wantKind {
		t.Fatalf("kind mismatch: got %q, want %q", c.kind, wantKind)
	}
	if c.val != wantVal {
		t.Fatalf("value mismatch for kind %s: got %#v, want %#v", wantKind, c.val, wantVal)
	}
}
