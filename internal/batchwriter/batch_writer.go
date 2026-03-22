// Package batchwriter provides BatchWriter, a single-goroutine batch dispatcher that
// preserves strict FIFO ordering across heterogeneous item types. Each item is dispatched
// individually in enqueue order. Supports configurable batch sizes, flush intervals,
// and optional NDJSON failure logging.
package batchwriter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sprawler/internal/logger"
)

// Item is a type-agnostic envelope carried on the input channel.
// Kind discriminates which registered operation should handle Value.
// The writer guarantees that items are executed in the exact order they were received.
type Item struct {
	Kind  string
	Value any
}

// opFn is a type-erased execution function. Each registered Kind maps to one opFn.
// The writer calls it once per item in strict FIFO order.
type opFn func(ctx context.Context, item any) error

// FlushHooks provides optional callbacks around each flush cycle.
// Before runs once before any ops; After runs once after all ops complete.
// Both are optional (nil means no-op). Set via SetFlushHooks before processing starts.
type FlushHooks struct {
	Before func(ctx context.Context) (context.Context, error)
	After  func(ctx context.Context, err error) error
}

// FailedBatch captures a failed flush for NDJSON failure logging.
type FailedBatch struct {
	When      time.Time `json:"when"`
	BatchSize int       `json:"batchSize"`
	Error     string    `json:"error"`
	Items     []any     `json:"items"`
}

// KindStats tracks per-[Kind] success and failure counts.
type KindStats struct {
	Successful int64
	Failed     int64
}

// neverTicks is a static nil channel for when ticker is disabled.
var neverTicks <-chan time.Time

// BatchWriter preserves a single, unified FIFO across heterogeneous item kinds.
// Producers send [Item] values via [BatchWriter.Add]. The writer flushes in strict
// FIFO order, dispatching each item individually.
//
// BatchWriter is safe for concurrent producers calling [BatchWriter.Add],
// but [BatchWriter.Close] must be called only after all producers have stopped.
type BatchWriter struct {
	logger        *logger.Logger
	maxBatchSize  int           // max total Items per flush (across kinds)
	flushInterval time.Duration // time-based flush
	onceClose     sync.Once

	// input & control
	in       chan Item     // single FIFO channel all producers send to via Add()
	flushReq chan struct{} // signals the run loop to perform an immediate flush
	flushAck chan struct{} // run loop sends back when the requested flush completes

	// registered operations by Kind
	ops   map[string]opFn // maps Kind string to its batch execution function
	opsMu sync.RWMutex
	hooks *FlushHooks // optional before/after hooks for each flush

	// run-loop state (only accessed by the single run goroutine, except buf)
	ctx    context.Context
	cancel context.CancelFunc
	ticker *time.Ticker
	buf    []Item // accumulates items between flushes
	wg     sync.WaitGroup

	// stats (guarded by mu, except totalQueued which is atomic)
	totalQueued    atomic.Int64
	mu             sync.Mutex
	successCount   int64
	failureCount   int64
	flushCount     int64
	totalWriteTime time.Duration
	kindStats      map[string]*KindStats

	// optional NDJSON failure log (guarded by failFileMu)
	failFilePath string
	failFileND   bool
	failFileMu   sync.Mutex
}

// New creates a BatchWriter and starts its background run loop.
//
// maxBatchSize caps how many items are buffered before triggering a flush.
// flushInterval triggers a time-based flush when no size threshold is reached.
// inputCap sets the buffered channel capacity, controlling backpressure on producers.
// All execution is single-threaded to preserve FIFO ordering.
func New(parent context.Context, maxBatchSize int, flushInterval time.Duration, inputCap int) *BatchWriter {
	if maxBatchSize <= 0 {
		maxBatchSize = 1000
	}
	if inputCap <= 0 {
		inputCap = maxBatchSize * 2
	}

	ctx, cancel := context.WithCancel(parent)
	w := &BatchWriter{
		logger:        logger.NewLogger("DB"),
		maxBatchSize:  maxBatchSize,
		flushInterval: flushInterval,
		in:            make(chan Item, inputCap),
		flushReq:      make(chan struct{}),
		flushAck:      make(chan struct{}),
		ops:           make(map[string]opFn),
		ctx:           ctx,
		cancel:        cancel,
		buf:           make([]Item, 0, maxBatchSize),
		kindStats:     make(map[string]*KindStats),
	}
	if flushInterval > 0 {
		w.ticker = time.NewTicker(flushInterval)
	}
	w.wg.Add(1)
	go w.run()
	return w
}

// Register binds a Kind string to an execution function.
// Prefer [RegisterTyped] for compile-time type safety.
func (w *BatchWriter) Register(kind string, exec func(context.Context, any) error) {
	w.opsMu.Lock()
	defer w.opsMu.Unlock()
	w.ops[kind] = exec
}

// Add sends a single item to the writer.
func (w *BatchWriter) Add(it Item) {
	w.in <- it
	w.totalQueued.Add(1)
}

// Flush performs a synchronous flush of items currently in the buffer.
//
// NOTE: This flushes only items already queued; items added concurrently
// during the flush may not be included. For quiescent behavior, ensure
// all producers have stopped before calling Flush().
func (w *BatchWriter) Flush() {
	select {
	case w.flushReq <- struct{}{}:
		<-w.flushAck
	case <-w.ctx.Done():
		return
	}
}

// EnableFailureJSON enables optional failure logging to a file.
// If ndjson is true, each failure is appended as a single JSON line immediately.
//
// IMPORTANT: Do not call this after processing has started to avoid file path races.
func (w *BatchWriter) EnableFailureJSON(path string, ndjson bool) {
	w.failFileMu.Lock()
	w.failFilePath = path
	w.failFileND = ndjson
	w.failFileMu.Unlock()
}

// SetFlushHooks sets optional before/after hooks that wrap each flush cycle.
//
// IMPORTANT: Call before processing starts, like EnableFailureJSON.
func (w *BatchWriter) SetFlushHooks(h *FlushHooks) {
	w.hooks = h
}

// Close drains, flushes remaining items in order, and stops the writer.
//
// IMPORTANT: Ensure all producers stop sending before calling Close() to avoid panics.
// Producers calling Add() after Close() starts will panic on closed channel.
func (w *BatchWriter) Close() {
	w.onceClose.Do(func() {
		//  Graceful signal: no more input; run-loop will drain and final-flush, then exit
		close(w.in)

		// Wait for run-loop to finish (ensures everything is flushed)
		// Must wait BEFORE cancelling context so final flush can complete
		w.wg.Wait()

		// Cancel context AFTER everything is flushed
		if w.cancel != nil {
			w.cancel()
		}

		// Stop periodic trigger AFTER goroutine is done
		if w.ticker != nil {
			w.ticker.Stop()
		}

		// Log final statistics on a single line.
		w.mu.Lock()
		succeeded, failed, flushes, writeTime := w.successCount, w.failureCount, w.flushCount, w.totalWriteTime
		w.mu.Unlock()

		line := fmt.Sprintf("Closed: %s written", compactCount(succeeded))
		if writeTime > 0 {
			line += fmt.Sprintf(" (%.0f/sec)", float64(succeeded)/writeTime.Seconds())
		}
		line += fmt.Sprintf(", %d flushes", flushes)
		if failed > 0 {
			line += fmt.Sprintf(", %d failed", failed)
		}

		var kindParts []string
		for kind, stats := range w.kindStats {
			kindParts = append(kindParts, fmt.Sprintf("%s: %s", kind, compactCount(stats.Successful)))
		}
		if len(kindParts) > 0 {
			line += " | " + strings.Join(kindParts, ", ")
		}

		w.logger.Infof("%s", line)

	})
}

// GetStats returns aggregate counters.
func (w *BatchWriter) GetStats() (successful, failed, flushes int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.successCount, w.failureCount, w.flushCount
}

// TotalQueued returns the total number of items ever enqueued.
func (w *BatchWriter) TotalQueued() int64 { return w.totalQueued.Load() }

// QueueLength returns the current number of items waiting in the input channel.
func (w *BatchWriter) QueueLength() int64 { return int64(len(w.in)) }

// GetKindStats returns a copy of per-kind statistics
func (w *BatchWriter) GetKindStats() map[string]KindStats {
	w.mu.Lock()
	defer w.mu.Unlock()

	result := make(map[string]KindStats)
	for kind, stats := range w.kindStats {
		result[kind] = *stats
	}
	return result
}

// --- internals ---

// run is the single-threaded event loop that owns buf and serializes all flushes.
// It exits when the input channel is closed and fully drained.
func (w *BatchWriter) run() {
	defer w.wg.Done()

	for {
		// Eagerly flush if buffer has hit capacity before blocking on select.
		if len(w.buf) >= w.maxBatchSize {
			w.flushLocked()
		}

		select {
		case item, ok := <-w.in:
			if !ok {
				w.flushLocked() // final flush after input channel closed
				return
			}
			w.buf = append(w.buf, item)
			if len(w.buf) >= w.maxBatchSize {
				w.flushLocked()
			}

		case <-w.flushReq:
			// Drain any items already buffered in the channel before flushing,
			// so the caller's Flush() captures as much pending work as possible.
		Drain:
			for {
				select {
				case item, ok := <-w.in:
					if !ok {
						break Drain
					}
					w.buf = append(w.buf, item)
					if len(w.buf) >= w.maxBatchSize {
						w.flushLocked()
					}
				default:
					break Drain
				}
			}
			w.flushLocked()
			w.flushAck <- struct{}{}

		case <-w.tickC():
			w.flushLocked() // periodic time-based flush
		}
	}
}

func (w *BatchWriter) tickC() <-chan time.Time {
	if w.ticker == nil {
		return neverTicks
	}
	return w.ticker.C
}

// flushLocked snapshots the buffer, resets it, then dispatches each item
// individually in strict FIFO order. If FlushHooks are set, Before/After
// wrap the entire flush cycle.
func (w *BatchWriter) flushLocked() {
	if len(w.buf) == 0 {
		return
	}

	// Snapshot and reset the buffer so the run loop can accumulate new items
	// while this flush executes.
	batch := make([]Item, len(w.buf))
	copy(batch, w.buf)
	w.buf = w.buf[:0]

	start := time.Now()

	// Before hook
	ctx := w.ctx
	if w.hooks != nil && w.hooks.Before != nil {
		var err error
		ctx, err = w.hooks.Before(ctx)
		if err != nil {
			w.logger.Errorf("flush before hook failed: %v", err)
			w.failFlush(batch, fmt.Errorf("flush before hook: %w", err))
			return
		}
	}

	var flushErr error

	for _, it := range batch {
		w.opsMu.RLock()
		fn, ok := w.ops[it.Kind]
		w.opsMu.RUnlock()

		if !ok {
			w.logger.Errorf("no operation registered for kind %q; dropping 1 item", it.Kind)
			flushErr = fmt.Errorf("no operation registered for kind %q", it.Kind)
			break
		}

		if err := fn(ctx, it.Value); err != nil {
			flushErr = err
			break
		}
	}

	// After hook
	if w.hooks != nil && w.hooks.After != nil {
		flushErr = w.hooks.After(ctx, flushErr)
	}

	elapsed := time.Since(start)

	if flushErr != nil {
		w.failFlush(batch, flushErr)
	} else {
		w.bump(true, int64(len(batch)), elapsed)
		for kind, n := range countByKind(batch) {
			w.bumpKind(kind, true, int64(n))
		}
	}
}

// failFlush records a complete flush failure, counting all items as failed.
func (w *BatchWriter) failFlush(batch []Item, err error) {
	w.recordFlushFailure(batch, err)
	w.bump(false, int64(len(batch)), 0)
	for kind, n := range countByKind(batch) {
		w.bumpKind(kind, false, int64(n))
	}
}

func countByKind(batch []Item) map[string]int {
	m := make(map[string]int)
	for _, it := range batch {
		m[it.Kind]++
	}
	return m
}

// bump updates global success/failure counters and accumulates write time.
func (w *BatchWriter) bump(success bool, n int64, elapsed time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Count all transactions, both successful and failed
	w.flushCount++

	if success {
		w.successCount += n
		w.totalWriteTime += elapsed
	} else {
		w.failureCount += n
	}
}

// bumpKind updates per-Kind success/failure counters.
func (w *BatchWriter) bumpKind(kind string, success bool, n int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.kindStats[kind] == nil {
		w.kindStats[kind] = &KindStats{}
	}
	if success {
		w.kindStats[kind].Successful += n
	} else {
		w.kindStats[kind].Failed += n
	}
}

// --- failure recording & file helpers ---

// recordFlushFailure logs the entire failed flush as a single FailedBatch to the NDJSON log.
func (w *BatchWriter) recordFlushFailure(batch []Item, err error) {
	items := make([]any, len(batch))
	for i, it := range batch {
		items[i] = it.Value
	}
	fb := FailedBatch{
		When:      time.Now(),
		BatchSize: len(batch),
		Error:     err.Error(),
		Items:     items,
	}
	w.appendFailureNDJSON(fb)
}

// appendFailureNDJSON serializes a FailedBatch as a single JSON line and appends it to the
// failure log file. Best-effort: errors are silently ignored.
func (w *BatchWriter) appendFailureNDJSON(fb FailedBatch) {
	w.failFileMu.Lock()
	path, nd := w.failFilePath, w.failFileND
	w.failFileMu.Unlock()
	if path == "" || !nd {
		return
	}

	// JSON-safe copy
	safe := FailedBatch{
		When:      fb.When,
		BatchSize: fb.BatchSize,
		Error:     fb.Error,
		Items:     make([]any, len(fb.Items)),
	}
	for i, it := range fb.Items {
		safe.Items[i] = jsonSafe(it)
	}

	b, err := json.Marshal(safe)
	if err != nil {
		return // best-effort
	}
	_ = appendLine(path, b) // best-effort; ignore errors
}

// jsonSafe tries to marshal a value; if it fails, returns a string representation.
func jsonSafe(v any) any {
	if _, err := json.Marshal(v); err != nil {
		return fmt.Sprintf("%T(%v)", v, v)
	}
	return v
}

// appendLine opens a file in append mode and writes b followed by a newline.
func appendLine(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		return err
	}
	_, err = f.Write([]byte("\n"))
	return err
}

// --- Typed kind helpers ---

// Kind binds a string key to a concrete type at compile time, so that
// RegisterTyped and Enqueue can enforce type consistency.
type Kind[T any] struct{ name string }

// NewKind creates a Kind that associates a string key with type T.
func NewKind[T any](name string) Kind[T] { return Kind[T]{name: name} }

// RegisterTyped adapts a strongly-typed function to the type-erased Register API.
// The Kind parameter ensures the registered function's type matches the Enqueue call site.
func RegisterTyped[T any](w *BatchWriter, kind Kind[T], exec func(context.Context, T) error) {
	w.Register(kind.name, func(ctx context.Context, v any) error {
		cast, ok := v.(T)
		if !ok {
			return fmt.Errorf("kind %q: unexpected type %T", kind.name, v)
		}
		return exec(ctx, cast)
	})
}

// Enqueue sends a single typed value. The Kind parameter ensures compile-time
// type consistency with the corresponding RegisterTyped call.
func Enqueue[T any](w *BatchWriter, kind Kind[T], v T) {
	w.Add(Item{Kind: kind.name, Value: v})
}

// compactCount formats a number compactly for log output.
func compactCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
