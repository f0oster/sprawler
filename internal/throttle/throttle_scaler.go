// Package throttle provides adaptive concurrency control based on backpressure signals.
package throttle

import (
	"sync/atomic"
	"time"

	"sprawler/internal/logger"
)

// ScaleAction defines how aggressively to reduce concurrency for a signal.
type ScaleAction int

// Scale-down actions, ordered from most to least aggressive.
const (
	ScaleHalve     ScaleAction = iota // halve concurrency
	ScaleReduceOne                    // reduce by one
)

// BackpressureSignal describes a single backpressure source the scaler monitors.
type BackpressureSignal struct {
	Name           string
	GetCount       func() int64
	Action         ScaleAction
	CooldownPeriod time.Duration
}

// signalState tracks per-signal monitoring state across ticks.
type signalState struct {
	signal    BackpressureSignal
	lastCount int64
	lastSeen  time.Time
}

// ScalerStats summarizes [ThrottleScaler] behavior over a run.
type ScalerStats struct {
	ScaleDownEvents int
	ScaleUpEvents   int
	MinLimit        int
	TimeReduced     time.Duration // total time spent below max concurrency
}

// ThrottleScaler dynamically adjusts worker concurrency based on backpressure signals.
// Workers call [ThrottleScaler.Acquire] and [ThrottleScaler.Release] around work units.
// A background monitor checks all signals each tick and applies the most aggressive
// scale-down action when any signal fires. Scale-up occurs only when all signals are
// quiet for their respective cooldown periods.
//
// ThrottleScaler is safe for concurrent use by multiple goroutines.
type ThrottleScaler struct {
	maxWorkers    int
	currentLimit  atomic.Int32
	liveTokens    atomic.Int32
	sem           chan struct{}
	checkInterval time.Duration
	signals       []signalState
	logger        *logger.Logger
	stopCh        chan struct{}

	// Summary stats (written only by monitorLoop goroutine, read after Stop)
	scaleDownEvents int
	scaleUpEvents   int
	minLimit        int
	reducedSince    time.Time // when we last dropped below max (zero = at max)
	timeReduced     time.Duration
}

// ScalerOption configures optional ThrottleScaler behavior.
type ScalerOption func(*ThrottleScaler)

// WithCheckInterval overrides the default 10s monitor tick interval.
func WithCheckInterval(d time.Duration) ScalerOption {
	return func(s *ThrottleScaler) { s.checkInterval = d }
}

// NewThrottleScaler creates a scaler with a semaphore pre-filled to maxWorkers tokens.
func NewThrottleScaler(maxWorkers int, signals []BackpressureSignal, logger *logger.Logger, opts ...ScalerOption) *ThrottleScaler {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	states := make([]signalState, len(signals))
	for i, sig := range signals {
		states[i] = signalState{
			signal:    sig,
			lastCount: sig.GetCount(),
		}
	}
	s := &ThrottleScaler{
		maxWorkers:    maxWorkers,
		sem:           make(chan struct{}, maxWorkers),
		checkInterval: 10 * time.Second,
		signals:       states,
		logger:        logger,
		stopCh:        make(chan struct{}),
		minLimit:      maxWorkers,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.currentLimit.Store(int32(maxWorkers))
	s.liveTokens.Store(int32(maxWorkers))
	// Pre-fill semaphore tokens
	for i := 0; i < maxWorkers; i++ {
		s.sem <- struct{}{}
	}
	return s
}

// Acquire blocks until a semaphore token is available or ctx is cancelled.
// Returns false if the context was cancelled.
func (s *ThrottleScaler) Acquire(ctx interface{ Done() <-chan struct{} }) bool {
	select {
	case <-s.sem:
		return true
	case <-ctx.Done():
		return false
	}
}

// Release returns a token to the semaphore, but only if liveTokens is at or below
// the current limit. If above the limit (concurrency was scaled down), the token is
// swallowed via CAS to reduce liveTokens.
func (s *ThrottleScaler) Release() {
	for {
		live := s.liveTokens.Load()
		limit := s.currentLimit.Load()
		if live > limit {
			if s.liveTokens.CompareAndSwap(live, live-1) {
				return // token swallowed
			}
			continue // CAS failed, retry
		}
		s.sem <- struct{}{} // at or below limit — return token
		return
	}
}

// Start launches the background monitor goroutine.
func (s *ThrottleScaler) Start() {
	go s.monitorLoop()
}

// Stop signals the monitor goroutine to exit.
func (s *ThrottleScaler) Stop() {
	close(s.stopCh)
}

// CurrentLimit returns the current concurrency limit (for diagnostics).
func (s *ThrottleScaler) CurrentLimit() int {
	return int(s.currentLimit.Load())
}

// MaxWorkers returns the maximum worker count the scaler was configured with.
func (s *ThrottleScaler) MaxWorkers() int {
	return s.maxWorkers
}

// Stats returns summary statistics for the scaler's lifetime.
// Call after Stop() for accurate results.
func (s *ThrottleScaler) Stats() ScalerStats {
	reduced := s.timeReduced
	if !s.reducedSince.IsZero() {
		reduced += time.Since(s.reducedSince)
	}
	return ScalerStats{
		ScaleDownEvents: s.scaleDownEvents,
		ScaleUpEvents:   s.scaleUpEvents,
		MinLimit:        s.minLimit,
		TimeReduced:     reduced,
	}
}

func (s *ThrottleScaler) monitorLoop() {
	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case t := <-ticker.C:
			s.tick(t)
		}
	}
}

// tick runs one monitoring cycle at the given wall-clock time.
func (s *ThrottleScaler) tick(now time.Time) {
	current := int(s.currentLimit.Load())

	// Check all signals — pick the most aggressive action if multiple fire
	bestAction := ScaleReduceOne // least aggressive default
	fired := false
	for i := range s.signals {
		st := &s.signals[i]
		count := st.signal.GetCount()
		delta := count - st.lastCount
		st.lastCount = count

		if delta > 0 {
			st.lastSeen = now
			fired = true
			if st.signal.Action < bestAction { // lower = more aggressive
				bestAction = st.signal.Action
			}
			s.logger.Infof("Throttle scaler: %s detected (delta=%d)", st.signal.Name, delta)
		}
	}

	if fired {
		var newLimit int
		switch bestAction {
		case ScaleHalve:
			newLimit = current / 2
		case ScaleReduceOne:
			newLimit = current - 1
		}
		if newLimit < 1 {
			newLimit = 1
		}
		if newLimit < current {
			s.scaleDown(current, newLimit)
			s.scaleDownEvents++
			if newLimit < s.minLimit {
				s.minLimit = newLimit
			}
			if s.reducedSince.IsZero() {
				s.reducedSince = now
			}
			s.logger.Infof("Throttle scaler: reducing concurrency %d -> %d", current, newLimit)
		}
		return
	}

	// Scale up only when ALL signals are quiet for their respective cooldowns
	if current >= s.maxWorkers {
		return
	}
	allQuiet := true
	for i := range s.signals {
		st := &s.signals[i]
		if !st.lastSeen.IsZero() && now.Sub(st.lastSeen) < st.signal.CooldownPeriod {
			allQuiet = false
			break
		}
	}
	// At least one signal must have fired previously before we scale up
	anyEverFired := false
	for i := range s.signals {
		if !s.signals[i].lastSeen.IsZero() {
			anyEverFired = true
			break
		}
	}
	if allQuiet && anyEverFired {
		newLimit := current + 1
		if newLimit > s.maxWorkers {
			newLimit = s.maxWorkers
		}
		s.scaleUp(current, newLimit)
		s.scaleUpEvents++
		if newLimit >= s.maxWorkers && !s.reducedSince.IsZero() {
			s.timeReduced += now.Sub(s.reducedSince)
			s.reducedSince = time.Time{}
		}
		// Reset cooldown timers for next scale-up
		for i := range s.signals {
			if !s.signals[i].lastSeen.IsZero() {
				s.signals[i].lastSeen = now
			}
		}
		s.logger.Infof("Throttle scaler: all signals quiet, increasing concurrency %d -> %d", current, newLimit)
	}
}

// scaleDown drains excess tokens from the semaphore to reduce effective concurrency.
func (s *ThrottleScaler) scaleDown(from, to int) {
	s.currentLimit.Store(int32(to))
	// Drain tokens non-blocking to enforce the new lower limit
	toDrain := from - to
	for i := 0; i < toDrain; i++ {
		select {
		case <-s.sem:
			s.liveTokens.Add(-1)
		default:
			// Token not available now; Release() will swallow it later
		}
	}
}

// scaleUp adds tokens to the semaphore to increase effective concurrency.
func (s *ThrottleScaler) scaleUp(from, to int) {
	s.currentLimit.Store(int32(to))
	toAdd := to - from
	for i := 0; i < toAdd; i++ {
		select {
		case s.sem <- struct{}{}:
			s.liveTokens.Add(1)
		default:
			// Semaphore buffer full; tokens will be restored as workers Release()
		}
	}
}
