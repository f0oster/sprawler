package metrics

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sprawler/internal/logger"
	"sprawler/internal/model"
)

// --- buildSnapshot tests ---

func TestReporter_BuildSnapshot_SiteProgress(t *testing.T) {
	m := NewRunMetrics("test")
	m.Start()

	completed := atomic.Int64{}
	completed.Store(50)

	r := NewProgressReporter(m, logger.NewLogger("test"),
		WithSiteProgress(func() SiteProgressSnapshot {
			return SiteProgressSnapshot{
				Completed:  completed.Load(),
				Failed:     2,
				InFlight:   5,
				Discovered: 100,
				Total:      200,
			}
		}),
	)

	snap := r.buildSnapshot()
	if snap.SiteProgress == nil {
		t.Fatal("SiteProgress is nil")
	}
	if snap.SiteProgress.Completed != 50 {
		t.Errorf("Completed = %d, want 50", snap.SiteProgress.Completed)
	}
	if snap.SiteProgress.Total != 200 {
		t.Errorf("Total = %d, want 200", snap.SiteProgress.Total)
	}
}

func TestReporter_BuildSnapshot_Entities(t *testing.T) {
	m := NewRunMetrics("test")
	m.Start()

	users := atomic.Int64{}
	users.Store(100)
	groups := atomic.Int64{}
	groups.Store(50)

	r := NewProgressReporter(m, logger.NewLogger("test"),
		WithEntities([]NamedCounter{
			{Label: "users", Counter: &users},
			{Label: "groups", Counter: &groups},
		}),
	)

	snap := r.buildSnapshot()
	if len(snap.Entities) != 2 {
		t.Fatalf("Entities len = %d, want 2", len(snap.Entities))
	}
	if snap.Entities[0].Value != 100 {
		t.Errorf("Entities[0].Value = %d, want 100", snap.Entities[0].Value)
	}
	if snap.Entities[1].Value != 50 {
		t.Errorf("Entities[1].Value = %d, want 50", snap.Entities[1].Value)
	}
}

func TestReporter_BuildSnapshot_Skips(t *testing.T) {
	m := NewRunMetrics("test")
	m.Start()

	teamChannel := atomic.Int64{}
	teamChannel.Store(10)

	r := NewProgressReporter(m, logger.NewLogger("test"),
		WithSkips([]NamedCounter{
			{Label: "team_channel", Counter: &teamChannel},
		}),
	)

	snap := r.buildSnapshot()
	if len(snap.Skips) != 1 {
		t.Fatalf("Skips len = %d, want 1", len(snap.Skips))
	}
	if snap.Skips[0].Value != 10 {
		t.Errorf("Skips[0].Value = %d, want 10", snap.Skips[0].Value)
	}
}

func TestReporter_WithActiveWorkers(t *testing.T) {
	m := NewRunMetrics("test")
	workers := &atomic.Int64{}

	r := NewProgressReporter(m, logger.NewLogger("test"),
		WithActiveWorkers(workers),
	)

	if r.activeWorkers != workers {
		t.Error("activeWorkers not set via option")
	}
}

func TestReporter_WindowedRate(t *testing.T) {
	m := NewRunMetrics("test")
	m.Start()

	completed := atomic.Int64{}

	r := NewProgressReporter(m, logger.NewLogger("test"),
		WithSiteProgress(func() SiteProgressSnapshot {
			return SiteProgressSnapshot{
				Completed: completed.Load(),
				Total:     1000,
			}
		}),
	)

	// First call -- cumulative fallback returns 0 (no items yet)
	snap := r.buildSnapshot()
	if snap.Rate != nil && *snap.Rate != 0 {
		t.Errorf("initial rate = %f, want nil or 0", *snap.Rate)
	}

	// Add items and wait
	completed.Store(100)
	time.Sleep(100 * time.Millisecond)

	snap = r.buildSnapshot()
	if snap.Rate == nil || *snap.Rate <= 0 {
		t.Errorf("rate after items = %v, want > 0", snap.Rate)
	}
}

func TestReporter_Scaler(t *testing.T) {
	m := NewRunMetrics("test")
	m.Start()

	r := NewProgressReporter(m, logger.NewLogger("test"),
		WithScaler(func() ScalerSnapshot {
			return ScalerSnapshot{Current: 5, Max: 10, ScaleDowns: 2}
		}),
	)

	snap := r.buildSnapshot()
	if snap.Scaler == nil {
		t.Fatal("Scaler is nil")
	}
	if snap.Scaler.Current != 5 || snap.Scaler.Max != 10 || snap.Scaler.ScaleDowns != 2 {
		t.Errorf("Scaler = %+v, want {5, 10, 2}", *snap.Scaler)
	}
}

// --- DefaultProgressFormatter tests ---

func ptr[T any](v T) *T { return &v }

func TestDefaultProgressFormatter_FullSnapshot(t *testing.T) {
	snap := ProgressSnapshot{
		SiteProgress: &SiteProgressSnapshot{
			Completed:  3258,
			Failed:     3,
			InFlight:   407,
			Discovered: 5000,
			Total:      250_000,
		},
		Scaler:   &ScalerSnapshot{Current: 6, Max: 6},
		Rate:     ptr(82.0),
		ETA:      ptr(50*time.Minute + 9*time.Second),
		Entities: []LabeledValue{{Label: "users", Value: 263_000}},
		Skips: []LabeledValue{
			{Label: "app_catalog", Value: 1},
			{Label: "tenant_root", Value: 1},
		},
		API: &model.APIMetrics{
			AvgDuration: 70,
			StatusCodes: map[int]int64{200: 3261},
		},
		Storage:  &model.StorageStats{ProcessedItems: 266_000, QueueLength: 0},
		Channels: []ChannelStat{{Name: "sites->users", Len: 400, Cap: 400}},
	}

	got := DefaultProgressFormatter(snap)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d:\n%s", len(lines), got)
	}

	// Line 1: sites + entities + skips
	assertContains(t, lines[0], "Sites: 3258/250k")
	assertContains(t, lines[0], "407 inflight")
	assertContains(t, lines[0], "3 failed")
	assertContains(t, lines[0], "263k users")
	assertContains(t, lines[0], "skip: 1 app_catalog, 1 tenant_root")

	// Line 2: workers + rate + ETA + API + DB + buffers
	assertContains(t, lines[1], "6/6 workers")
	assertContains(t, lines[1], "82.0/sec")
	assertContains(t, lines[1], "ETA: 50m 9s")
	assertContains(t, lines[1], "API: 3261 req 70ms [200:3261]")
	assertContains(t, lines[1], "DB: 266k, 0 queued")
	assertContains(t, lines[1], "Buf: sites->users 400/400")
}

func TestDefaultProgressFormatter_SitesOnly(t *testing.T) {
	snap := ProgressSnapshot{
		SiteProgress: &SiteProgressSnapshot{
			Completed: 100,
			Total:     1000,
		},
	}

	got := DefaultProgressFormatter(snap)
	lines := strings.Split(got, "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d:\n%s", len(lines), got)
	}
	assertContains(t, lines[0], "Sites: 100/1000")
	// No parenthetical when inflight/failed are all zero
	if strings.Contains(lines[0], "(") {
		t.Errorf("expected no parenthetical, got: %s", lines[0])
	}
}

func TestDefaultProgressFormatter_SitesNoTotal(t *testing.T) {
	snap := ProgressSnapshot{
		SiteProgress: &SiteProgressSnapshot{
			Completed:  500,
			Discovered: 2000,
		},
	}

	got := DefaultProgressFormatter(snap)
	if strings.Contains(got, "/") {
		t.Errorf("expected no /total when Total=0, got: %s", got)
	}
	assertContains(t, got, "Sites: 500")
}

func TestDefaultProgressFormatter_EmptySnapshot(t *testing.T) {
	got := DefaultProgressFormatter(ProgressSnapshot{})
	if got != "" {
		t.Errorf("expected empty string for empty snapshot, got: %q", got)
	}
}

func TestDefaultProgressFormatter_Line2OmittedWhenNoInfra(t *testing.T) {
	snap := ProgressSnapshot{
		SiteProgress: &SiteProgressSnapshot{Completed: 10, Total: 100},
		Entities:     []LabeledValue{{Label: "users", Value: 42}},
	}

	got := DefaultProgressFormatter(snap)
	lines := strings.Split(got, "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line (sites+entities, no infra), got %d:\n%s", len(lines), got)
	}
	assertContains(t, lines[0], "Sites:")
	assertContains(t, lines[0], "42 users")
}

func TestDefaultProgressFormatter_ZeroValueEntitiesSkipped(t *testing.T) {
	snap := ProgressSnapshot{
		SiteProgress: &SiteProgressSnapshot{Completed: 1},
		Entities: []LabeledValue{
			{Label: "users", Value: 0},
			{Label: "groups", Value: 5},
		},
		Skips: []LabeledValue{
			{Label: "app_catalog", Value: 0},
		},
	}

	got := DefaultProgressFormatter(snap)
	// entities and skips are on line 1 now
	assertContains(t, got, "5 groups")
	if strings.Contains(got, "users") {
		t.Errorf("zero-value entity should be omitted: %s", got)
	}
	if strings.Contains(got, "skip:") {
		t.Errorf("skip section should be omitted when all zero: %s", got)
	}
}

func TestDefaultProgressFormatter_WorkersWithoutScaler(t *testing.T) {
	snap := ProgressSnapshot{
		SiteProgress: &SiteProgressSnapshot{Completed: 1},
		Workers:      ptr(int64(8)),
	}

	got := DefaultProgressFormatter(snap)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d:\n%s", len(lines), got)
	}
	assertContains(t, lines[1], "8 workers")
}

func TestDefaultProgressFormatter_ScalerWithScaleDowns(t *testing.T) {
	snap := ProgressSnapshot{
		SiteProgress: &SiteProgressSnapshot{Completed: 1},
		Scaler:       &ScalerSnapshot{Current: 4, Max: 10, ScaleDowns: 3},
	}

	got := DefaultProgressFormatter(snap)
	assertContains(t, got, "4/10 workers (3 scaled)")
}

func TestDefaultProgressFormatter_ETAFormats(t *testing.T) {
	tests := []struct {
		name string
		eta  time.Duration
		want string
	}{
		{"seconds", 45 * time.Second, "ETA: 45s"},
		{"minutes", 5*time.Minute + 30*time.Second, "ETA: 5m 30s"},
		{"hours", 2*time.Hour + 15*time.Minute, "ETA: 2h 15m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := ProgressSnapshot{
				SiteProgress: &SiteProgressSnapshot{Completed: 1, Total: 100},
				Rate:         ptr(1.0),
				ETA:          &tt.eta,
			}
			got := DefaultProgressFormatter(snap)
			assertContains(t, got, tt.want)
		})
	}
}

func TestDefaultProgressFormatter_APITransportRetries(t *testing.T) {
	snap := ProgressSnapshot{
		SiteProgress: &SiteProgressSnapshot{Completed: 1},
		API: &model.APIMetrics{
			AvgDuration:      100,
			StatusCodes:      map[int]int64{200: 50, 429: 5},
			TransportRetries: 3,
		},
	}

	got := DefaultProgressFormatter(snap)
	lines := strings.Split(got, "\n")
	last := lines[len(lines)-1]
	assertContains(t, last, "200:50")
	assertContains(t, last, "429:5")
	assertContains(t, last, "3 transport retries")
}

func TestDefaultProgressFormatter_ChannelsOnlyNonEmpty(t *testing.T) {
	snap := ProgressSnapshot{
		SiteProgress: &SiteProgressSnapshot{Completed: 1},
		Channels: []ChannelStat{
			{Name: "a->b", Len: 10, Cap: 100},
			{Name: "b->c", Len: 0, Cap: 100}, // empty, should be omitted
		},
	}

	got := DefaultProgressFormatter(snap)
	assertContains(t, got, "Buf: a->b 10/100")
	if strings.Contains(got, "b->c") {
		t.Errorf("empty channel should be omitted: %s", got)
	}
}

func TestDefaultProgressFormatter_EntitiesAndSkipsOnLine1(t *testing.T) {
	snap := ProgressSnapshot{
		SiteProgress: &SiteProgressSnapshot{Completed: 1},
		Entities:     []LabeledValue{{Label: "users", Value: 100}},
		Skips:        []LabeledValue{{Label: "app_catalog", Value: 2}},
	}

	got := DefaultProgressFormatter(snap)
	lines := strings.Split(got, "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d:\n%s", len(lines), got)
	}
	assertContains(t, lines[0], "100 users")
	assertContains(t, lines[0], "skip: 2 app_catalog")
}

// --- formatCompact tests ---

func TestFormatCompact(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{9999, "9999"},
		{10_000, "10k"},
		{55_500, "55k"},
		{999_999, "999k"},
		{1_000_000, "1.0M"},
		{1_500_000, "1.5M"},
		{25_000_000, "25.0M"},
	}

	for _, tt := range tests {
		got := formatCompact(tt.n)
		if got != tt.want {
			t.Errorf("formatCompact(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

// --- helpers ---

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected %q to contain %q", s, substr)
	}
}
