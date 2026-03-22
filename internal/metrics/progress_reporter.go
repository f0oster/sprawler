package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sprawler/internal/logger"
)

// ProgressReporter generates periodic progress updates by sampling data from
// closures and named counters, then passing a ProgressSnapshot to a formatter.
type ProgressReporter struct {
	metrics   *RunMetrics
	logger    *logger.Logger
	formatter ProgressFormatter
	stopCh    chan struct{}
	done      chan struct{}
	once      sync.Once

	// Section data sources (nil/empty = section omitted)
	siteProgress  func() SiteProgressSnapshot
	entities      []NamedCounter
	skips         []NamedCounter
	scaler        func() ScalerSnapshot
	activeWorkers *atomic.Int64
	channelStats  func() []ChannelStat

	// Windowed rate tracking
	prevCompleted int64
	prevTime      time.Time
}

// ReporterOption configures a ProgressReporter instance.
type ReporterOption func(*ProgressReporter)

// NewProgressReporter creates a progress reporter.
func NewProgressReporter(metrics *RunMetrics, logger *logger.Logger, opts ...ReporterOption) *ProgressReporter {
	r := &ProgressReporter{
		metrics:   metrics,
		logger:    logger,
		formatter: DefaultProgressFormatter,
		stopCh:    make(chan struct{}),
		done:      make(chan struct{}),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// WithSiteProgress sets a closure that returns site progress data each tick.
func WithSiteProgress(fn func() SiteProgressSnapshot) ReporterOption {
	return func(r *ProgressReporter) { r.siteProgress = fn }
}

// WithEntities sets the named counters shown in the entity section.
func WithEntities(counters []NamedCounter) ReporterOption {
	return func(r *ProgressReporter) { r.entities = counters }
}

// WithSkips sets the named counters shown in the skip section.
func WithSkips(counters []NamedCounter) ReporterOption {
	return func(r *ProgressReporter) { r.skips = counters }
}

// WithScaler sets a closure that returns throttle scaler state each tick.
func WithScaler(fn func() ScalerSnapshot) ReporterOption {
	return func(r *ProgressReporter) { r.scaler = fn }
}

// WithActiveWorkers sets the active worker counter.
func WithActiveWorkers(w *atomic.Int64) ReporterOption {
	return func(r *ProgressReporter) { r.activeWorkers = w }
}

// WithChannelStats sets a closure that returns pipeline channel utilization each tick.
func WithChannelStats(fn func() []ChannelStat) ReporterOption {
	return func(r *ProgressReporter) { r.channelStats = fn }
}

// WithFormatter sets a custom progress formatter.
func WithFormatter(f ProgressFormatter) ReporterOption {
	return func(r *ProgressReporter) { r.formatter = f }
}

// Start begins periodic progress reporting at the given interval.
func (r *ProgressReporter) Start(interval time.Duration) {
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-r.stopCh:
				return
			case <-ticker.C:
				r.Report()
			}
		}
	}()
}

// Stop signals the reporter to stop and blocks until the goroutine exits.
func (r *ProgressReporter) Stop() {
	r.once.Do(func() {
		close(r.stopCh)
	})
	<-r.done
}

// Report generates and logs a single progress report. The formatter returns
// newline-separated lines; the first gets the full prefix, continuations are indented.
func (r *ProgressReporter) Report() {
	snap := r.buildSnapshot()
	message := r.formatter(snap)
	lines := strings.Split(message, "\n")
	r.logger.Infof("[%s] %s", snap.Elapsed, lines[0])
	for _, line := range lines[1:] {
		r.logger.Continuef("%s", line)
	}
}

// buildSnapshot gathers data from all configured sources into a ProgressSnapshot.
func (r *ProgressReporter) buildSnapshot() ProgressSnapshot {
	snap := ProgressSnapshot{
		Name:    r.metrics.name,
		Elapsed: r.metrics.Duration().Truncate(time.Second),
	}

	if r.siteProgress != nil {
		sp := r.siteProgress()
		snap.SiteProgress = &sp
	}

	if len(r.entities) > 0 {
		for _, e := range r.entities {
			snap.Entities = append(snap.Entities, LabeledValue{
				Label: e.Label,
				Value: e.Counter.Load(),
			})
		}
	}

	if len(r.skips) > 0 {
		for _, s := range r.skips {
			snap.Skips = append(snap.Skips, LabeledValue{
				Label: s.Label,
				Value: s.Counter.Load(),
			})
		}
	}

	if r.scaler != nil {
		ss := r.scaler()
		snap.Scaler = &ss
	}

	if r.activeWorkers != nil {
		w := r.activeWorkers.Load()
		snap.Workers = &w
	}

	// Windowed rate from SiteProgress.Completed
	if snap.SiteProgress != nil {
		if rate := r.computeWindowedRate(snap.SiteProgress.Completed); rate > 0 {
			snap.Rate = &rate
			if snap.SiteProgress.Total > 0 {
				remaining := float64(snap.SiteProgress.Total - snap.SiteProgress.Completed)
				eta := time.Duration(remaining / rate * float64(time.Second))
				snap.ETA = &eta
			}
		}
	}

	if r.metrics.apiStats != nil {
		api := r.metrics.apiStats.GetMetrics()
		snap.API = &api
	}
	if r.metrics.storageStats != nil {
		ss := r.metrics.storageStats()
		snap.Storage = &ss
	}
	if r.channelStats != nil {
		snap.Channels = r.channelStats()
	}

	return snap
}

// computeWindowedRate returns items/sec based on the delta since the previous tick.
// Falls back to cumulative rate on the first call.
func (r *ProgressReporter) computeWindowedRate(current int64) float64 {
	now := time.Now()
	defer func() {
		r.prevCompleted = current
		r.prevTime = now
	}()

	if r.prevTime.IsZero() {
		elapsed := r.metrics.Duration().Seconds()
		if elapsed == 0 {
			return 0
		}
		return float64(current) / elapsed
	}

	dt := now.Sub(r.prevTime).Seconds()
	if dt == 0 {
		return 0
	}
	return float64(current-r.prevCompleted) / dt
}

// DefaultProgressFormatter produces a 2-line progress report.
// Line 1: sites + entities + skips. Line 2: workers/rate/ETA + API + DB + buffers.
// Line 2 is omitted when empty.
func DefaultProgressFormatter(snap ProgressSnapshot) string {
	var lines []string

	// --- Line 1: Sites + entities + skips ---
	var line1 []string

	if snap.SiteProgress != nil {
		sp := snap.SiteProgress
		s := "Sites: " + formatCompact(sp.Completed)
		if sp.Total > 0 {
			s += "/" + formatCompact(sp.Total)
		}
		var parts []string
		if sp.InFlight > 0 {
			parts = append(parts, fmt.Sprintf("%d inflight", sp.InFlight))
		}
		if sp.Failed > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", sp.Failed))
		}
		if len(parts) > 0 {
			s += " (" + strings.Join(parts, ", ") + ")"
		}
		line1 = append(line1, s)
	}

	if len(snap.Entities) > 0 {
		var parts []string
		for _, e := range snap.Entities {
			if e.Value > 0 {
				parts = append(parts, fmt.Sprintf("%s %s", formatCompact(e.Value), e.Label))
			}
		}
		if len(parts) > 0 {
			line1 = append(line1, strings.Join(parts, ", "))
		}
	}

	if len(snap.Skips) > 0 {
		var parts []string
		for _, s := range snap.Skips {
			if s.Value > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", s.Value, s.Label))
			}
		}
		if len(parts) > 0 {
			line1 = append(line1, "skip: "+strings.Join(parts, ", "))
		}
	}

	if len(line1) > 0 {
		lines = append(lines, strings.Join(line1, " | "))
	}

	// --- Line 2: Workers/rate/ETA + infrastructure (API + DB + buffers) ---
	var line2 []string

	if snap.Scaler != nil {
		s := fmt.Sprintf("%d/%d workers", snap.Scaler.Current, snap.Scaler.Max)
		if snap.Scaler.ScaleDowns > 0 {
			s += fmt.Sprintf(" (%d scaled)", snap.Scaler.ScaleDowns)
		}
		line2 = append(line2, s)
	} else if snap.Workers != nil {
		line2 = append(line2, fmt.Sprintf("%d workers", *snap.Workers))
	}

	if snap.Rate != nil {
		line2 = append(line2, fmt.Sprintf("%.1f/sec", *snap.Rate))
		if snap.ETA != nil {
			eta := *snap.ETA
			if eta >= time.Hour {
				line2 = append(line2, fmt.Sprintf("ETA: %dh %dm", int(eta.Hours()), int(eta.Minutes())%60))
			} else if eta >= time.Minute {
				line2 = append(line2, fmt.Sprintf("ETA: %dm %ds", int(eta.Minutes()), int(eta.Seconds())%60))
			} else {
				line2 = append(line2, fmt.Sprintf("ETA: %ds", int(eta.Seconds())))
			}
		}
	}

	if snap.API != nil && len(snap.API.StatusCodes) > 0 {
		var reqCount int64
		var codes []int
		for code, count := range snap.API.StatusCodes {
			codes = append(codes, code)
			reqCount += count
		}
		sort.Ints(codes)
		var parts []string
		for _, code := range codes {
			parts = append(parts, fmt.Sprintf("%d:%d", code, snap.API.StatusCodes[code]))
		}
		apiSection := fmt.Sprintf("API: %d req %dms [%s]",
			reqCount, snap.API.AvgDuration, strings.Join(parts, " "))
		if snap.API.TransportRetries > 0 {
			apiSection += fmt.Sprintf(", %d transport retries", snap.API.TransportRetries)
		}
		line2 = append(line2, apiSection)
	}

	if snap.Storage != nil && (snap.Storage.ProcessedItems > 0 || snap.Storage.QueueLength > 0) {
		line2 = append(line2, fmt.Sprintf("DB: %s, %d queued",
			formatCompact(snap.Storage.ProcessedItems), snap.Storage.QueueLength))
	}

	if len(snap.Channels) > 0 {
		var chParts []string
		for _, cs := range snap.Channels {
			if cs.Len > 0 {
				chParts = append(chParts, fmt.Sprintf("%s %d/%d", cs.Name, cs.Len, cs.Cap))
			}
		}
		if len(chParts) > 0 {
			line2 = append(line2, "Buf: "+strings.Join(chParts, ", "))
		}
	}

	if len(line2) > 0 {
		lines = append(lines, strings.Join(line2, " | "))
	}

	return strings.Join(lines, "\n")
}

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
