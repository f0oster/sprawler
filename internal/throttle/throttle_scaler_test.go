package throttle

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"sprawler/internal/logger"
)

// counterSignal returns a BackpressureSignal backed by an atomic counter.
func counterSignal(name string, action ScaleAction, cooldown time.Duration) (BackpressureSignal, *atomic.Int64) {
	var counter atomic.Int64
	return BackpressureSignal{
		Name:           name,
		GetCount:       counter.Load,
		Action:         action,
		CooldownPeriod: cooldown,
	}, &counter
}

func newTestScaler(maxWorkers int, signals []BackpressureSignal, opts ...ScalerOption) *ThrottleScaler {
	return NewThrottleScaler(maxWorkers, signals, logger.NewLogger("test"), opts...)
}

// --- Option B: deterministic tick() tests ---

func TestTick_ScaleHalve(t *testing.T) {
	sig, counter := counterSignal("throttle", ScaleHalve, time.Minute)
	s := newTestScaler(10, []BackpressureSignal{sig})

	counter.Store(5)
	s.tick(time.Now())

	if got := s.CurrentLimit(); got != 5 {
		t.Fatalf("expected limit 5, got %d", got)
	}
}

func TestTick_ScaleReduceOne(t *testing.T) {
	sig, counter := counterSignal("err", ScaleReduceOne, time.Minute)
	s := newTestScaler(10, []BackpressureSignal{sig})

	counter.Store(1)
	s.tick(time.Now())

	if got := s.CurrentLimit(); got != 9 {
		t.Fatalf("expected limit 9, got %d", got)
	}
}

func TestTick_FloorAtOne(t *testing.T) {
	sig, counter := counterSignal("throttle", ScaleHalve, time.Minute)
	s := newTestScaler(2, []BackpressureSignal{sig})

	// Halve 2 -> 1
	counter.Store(1)
	s.tick(time.Now())
	// Halve 1 -> 0 floors at 1
	counter.Store(2)
	s.tick(time.Now())

	if got := s.CurrentLimit(); got != 1 {
		t.Fatalf("expected limit 1, got %d", got)
	}
}

func TestTick_MostAggressiveWins(t *testing.T) {
	sigH, counterH := counterSignal("throttle", ScaleHalve, time.Minute)
	sigR, counterR := counterSignal("err", ScaleReduceOne, time.Minute)
	s := newTestScaler(10, []BackpressureSignal{sigH, sigR})

	counterH.Store(1)
	counterR.Store(1)
	s.tick(time.Now())

	if got := s.CurrentLimit(); got != 5 {
		t.Fatalf("expected limit 5 (halve wins), got %d", got)
	}
}

func TestTick_IdleTokensDrained(t *testing.T) {
	sig, counter := counterSignal("throttle", ScaleHalve, time.Minute)
	s := newTestScaler(10, []BackpressureSignal{sig})

	counter.Store(1)
	s.tick(time.Now())

	// All 10 tokens were idle, so scaleDown should have drained 5.
	// liveTokens should now be 5.
	if got := s.liveTokens.Load(); got != 5 {
		t.Fatalf("expected liveTokens 5, got %d", got)
	}
}

// All 4 tokens are held when scaleDown fires, so the sem is empty and
// nothing can be drained. liveTokens(4) > limit(2), so the first two
// Release calls must CAS-swallow their tokens instead of returning them.
func TestTick_HeldTokensSwallowedOnRelease(t *testing.T) {
	sig, counter := counterSignal("throttle", ScaleHalve, time.Minute)
	s := newTestScaler(4, []BackpressureSignal{sig})

	// Acquire all 4 tokens -- sem is now empty, all tokens held.
	ctx := context.Background()
	s.Acquire(ctx)
	s.Acquire(ctx)
	s.Acquire(ctx)
	s.Acquire(ctx)

	counter.Store(1)
	s.tick(time.Now()) // limit 4 -> 2, no idle tokens to drain

	// Nothing drained, liveTokens unchanged at 4.
	if got := s.liveTokens.Load(); got != 4 {
		t.Fatalf("expected liveTokens 4 (nothing drained), got %d", got)
	}

	// First two releases: liveTokens(4) > limit(2), CAS-swallow path.
	s.Release()
	s.Release()

	if got := s.liveTokens.Load(); got != 2 {
		t.Fatalf("expected liveTokens 2 after swallowing, got %d", got)
	}

	// Remaining two releases: liveTokens(2) == limit(2), normal sem return.
	s.Release()
	s.Release()

	if got := s.liveTokens.Load(); got != 2 {
		t.Fatalf("expected liveTokens 2 after normal release, got %d", got)
	}
}

func TestTick_NoScaleUpDuringCooldown(t *testing.T) {
	cooldown := time.Minute
	sig, counter := counterSignal("throttle", ScaleHalve, cooldown)
	s := newTestScaler(10, []BackpressureSignal{sig})

	now := time.Now()
	counter.Store(1)
	s.tick(now) // 10 -> 5

	// Tick again during cooldown - should not scale up
	s.tick(now.Add(30 * time.Second))

	if got := s.CurrentLimit(); got != 5 {
		t.Fatalf("expected limit 5 (still in cooldown), got %d", got)
	}
}

// Setting currentLimit directly simulates a state unreachable via signals,
// isolating the "anyEverFired" guard that prevents scale-up when no signal
// has ever fired.
func TestTick_NoScaleUpIfNeverFired(t *testing.T) {
	sig, _ := counterSignal("throttle", ScaleHalve, time.Minute)
	s := newTestScaler(10, []BackpressureSignal{sig})

	// Force limit below max without signal firing
	s.currentLimit.Store(5)

	s.tick(time.Now().Add(10 * time.Minute))

	if got := s.CurrentLimit(); got != 5 {
		t.Fatalf("expected limit 5 (no signal ever fired), got %d", got)
	}
}

func TestTick_ScaleUpAfterCooldown(t *testing.T) {
	cooldown := time.Minute
	sig, counter := counterSignal("throttle", ScaleHalve, cooldown)
	s := newTestScaler(10, []BackpressureSignal{sig})

	now := time.Now()
	counter.Store(1)
	s.tick(now) // 10 -> 5

	// After cooldown expires
	s.tick(now.Add(cooldown + time.Second))

	if got := s.CurrentLimit(); got != 6 {
		t.Fatalf("expected limit 6 (scale up by 1), got %d", got)
	}
}

func TestTick_ScaleUpCapsAtMax(t *testing.T) {
	cooldown := time.Minute
	sig, counter := counterSignal("err", ScaleReduceOne, cooldown)
	s := newTestScaler(3, []BackpressureSignal{sig})

	now := time.Now()
	counter.Store(1)
	s.tick(now) // 3 -> 2

	// Scale up repeatedly past max
	for i := 1; i <= 5; i++ {
		s.tick(now.Add(cooldown*time.Duration(i) + time.Second))
	}

	if got := s.CurrentLimit(); got != 3 {
		t.Fatalf("expected limit 3 (capped at max), got %d", got)
	}
}

// Scale-up resets all cooldown timers to its own timestamp, so a subsequent
// scale-down starts a fresh cooldown window measured from the scale-up time.
func TestTick_CooldownResetAfterScaleUp(t *testing.T) {
	cooldown := time.Minute
	sig, counter := counterSignal("throttle", ScaleReduceOne, cooldown)
	s := newTestScaler(10, []BackpressureSignal{sig})

	now := time.Now()
	counter.Store(1)
	s.tick(now) // 10 -> 9

	// First scale-up after cooldown
	t1 := now.Add(cooldown + time.Second)
	s.tick(t1) // 9 -> 10

	// Trigger another scale-down
	counter.Store(2)
	s.tick(t1.Add(time.Second)) // 10 -> 9

	// Try to scale up before new cooldown expires (relative to t1, the reset point)
	s.tick(t1.Add(30 * time.Second))

	if got := s.CurrentLimit(); got != 9 {
		t.Fatalf("expected limit 9 (cooldown was reset), got %d", got)
	}
}

func TestTick_OneInCooldownBlocksScaleUp(t *testing.T) {
	sig1, counter1 := counterSignal("fast", ScaleHalve, 30*time.Second)
	sig2, counter2 := counterSignal("slow", ScaleHalve, 2*time.Minute)
	s := newTestScaler(10, []BackpressureSignal{sig1, sig2})

	now := time.Now()
	counter1.Store(1)
	counter2.Store(1)
	s.tick(now) // both fire, 10 -> 5

	// After fast cooldown but before slow cooldown
	s.tick(now.Add(time.Minute))

	if got := s.CurrentLimit(); got != 5 {
		t.Fatalf("expected limit 5 (slow signal still in cooldown), got %d", got)
	}
}

func TestTick_StatsCorrect(t *testing.T) {
	cooldown := time.Minute
	sig, counter := counterSignal("throttle", ScaleHalve, cooldown)
	s := newTestScaler(10, []BackpressureSignal{sig})

	now := time.Now()
	counter.Store(1)
	s.tick(now) // 10 -> 5 (down event 1)

	counter.Store(2)
	s.tick(now.Add(time.Second)) // 5 -> 2 (down event 2)

	// Scale up
	s.tick(now.Add(cooldown + 2*time.Second)) // 2 -> 3 (up event 1)

	stats := s.Stats()
	if stats.ScaleDownEvents != 2 {
		t.Fatalf("expected 2 scale-down events, got %d", stats.ScaleDownEvents)
	}
	if stats.ScaleUpEvents != 1 {
		t.Fatalf("expected 1 scale-up event, got %d", stats.ScaleUpEvents)
	}
	if stats.MinLimit != 2 {
		t.Fatalf("expected min limit 2, got %d", stats.MinLimit)
	}
}

// TimeReduced accumulates only when the limit returns to max, not continuously.
// Two full down/up cycles each contribute their own duration independently.
func TestTick_TimeReducedAccumulates(t *testing.T) {
	cooldown := time.Minute
	sig, counter := counterSignal("throttle", ScaleReduceOne, cooldown)
	s := newTestScaler(2, []BackpressureSignal{sig})

	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	counter.Store(1)
	s.tick(t0) // 2 -> 1, reducedSince = t0

	// Scale back up to max
	t1 := t0.Add(cooldown + time.Second)
	s.tick(t1) // 1 -> 2, accumulates (t1 - t0)

	// Trigger another cycle
	counter.Store(2)
	t2 := t1.Add(time.Second)
	s.tick(t2) // 2 -> 1

	t3 := t2.Add(cooldown + time.Second)
	s.tick(t3) // 1 -> 2, accumulates (t3 - t2)

	stats := s.Stats()
	expected := (t1.Sub(t0)) + (t3.Sub(t2))
	if stats.TimeReduced != expected {
		t.Fatalf("expected TimeReduced %v, got %v", expected, stats.TimeReduced)
	}
}

func TestTick_MaxWorkersOne(t *testing.T) {
	sig, counter := counterSignal("throttle", ScaleHalve, time.Minute)
	s := newTestScaler(1, []BackpressureSignal{sig})

	counter.Store(1)
	s.tick(time.Now())

	if got := s.CurrentLimit(); got != 1 {
		t.Fatalf("expected limit 1, got %d", got)
	}
}

func TestAcquire_ReturnsTrue(t *testing.T) {
	sig, _ := counterSignal("throttle", ScaleHalve, time.Minute)
	s := newTestScaler(2, []BackpressureSignal{sig})

	ctx := context.Background()
	if !s.Acquire(ctx) {
		t.Fatal("expected Acquire to return true")
	}
}

func TestAcquire_ReturnsFalseOnCancel(t *testing.T) {
	sig, _ := counterSignal("throttle", ScaleHalve, time.Minute)
	s := newTestScaler(1, []BackpressureSignal{sig})

	ctx := context.Background()
	s.Acquire(ctx) // take the only token

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if s.Acquire(cancelCtx) {
		t.Fatal("expected Acquire to return false on cancelled context")
	}
}

func TestRelease_ReturnsToken(t *testing.T) {
	sig, _ := counterSignal("throttle", ScaleHalve, time.Minute)
	s := newTestScaler(1, []BackpressureSignal{sig})

	ctx := context.Background()
	s.Acquire(ctx)
	s.Release()

	// Token should be available again
	if !s.Acquire(ctx) {
		t.Fatal("expected Acquire to succeed after Release")
	}
}

// --- Option A: integration smoke test ---

func TestMonitorLoop_Smoke(t *testing.T) {
	sig, counter := counterSignal("throttle", ScaleHalve, 10*time.Millisecond)
	s := newTestScaler(10, []BackpressureSignal{sig}, WithCheckInterval(10*time.Millisecond))
	s.Start()
	defer s.Stop()

	counter.Store(1)

	deadline := time.After(2 * time.Second)
	for {
		if s.CurrentLimit() < 10 {
			return // success
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for limit to decrease")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
