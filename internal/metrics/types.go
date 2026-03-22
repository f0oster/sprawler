// Package metrics provides progress reporting and run-level metrics collection.
package metrics

import (
	"sync/atomic"
	"time"

	"sprawler/internal/model"
)

// NamedCounter binds a display label to a live atomic counter for progress reporting.
type NamedCounter struct {
	Label   string
	Counter *atomic.Int64
}

// LabeledValue is a snapshot of a labeled count, used for display.
type LabeledValue struct {
	Label string
	Value int64
}

// SiteProgressSnapshot holds point-in-time site processing state.
type SiteProgressSnapshot struct {
	Completed  int64
	Failed     int64
	InFlight   int64
	Discovered int64
	Total      int64
}

// ScalerSnapshot holds point-in-time throttle scaler state.
type ScalerSnapshot struct {
	Current    int
	Max        int
	ScaleDowns int
}

// ProgressSnapshot contains the data needed to render a progress report.
// Optional sections are nil when unavailable.
//
// The snapshot is assembled from independent atomic reads rather than a
// single transactional read, so fields may reflect slightly different moments
// in time. This is acceptable for progress reporting.
type ProgressSnapshot struct {
	Name    string
	Elapsed time.Duration

	SiteProgress *SiteProgressSnapshot
	Entities     []LabeledValue
	Skips        []LabeledValue
	Scaler       *ScalerSnapshot

	Workers *int64
	Rate    *float64
	ETA     *time.Duration

	API      *model.APIMetrics
	Storage  *model.StorageStats
	Channels []ChannelStat
}

// ChannelStat captures point-in-time utilization of a pipeline channel.
type ChannelStat struct {
	Name string
	Len  int
	Cap  int
}

// ProgressFormatter converts a ProgressSnapshot into a display string.
type ProgressFormatter func(ProgressSnapshot) string
