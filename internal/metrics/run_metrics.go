package metrics

import (
	"time"

	"sprawler/internal/model"
)

// APIMetricsProvider returns a snapshot of API call statistics.
type APIMetricsProvider interface {
	GetMetrics() model.APIMetrics
}

// RunMetrics captures run timing and external data source references.
type RunMetrics struct {
	name      string
	startTime time.Time
	endTime   time.Time

	// External read-only sources
	apiStats        APIMetricsProvider
	storageStats    func() model.StorageStats
	storageBaseline model.StorageStats
}

// RunMetricsOption configures a RunMetrics instance.
type RunMetricsOption func(*RunMetrics)

// NewRunMetrics creates a new RunMetrics with the given options.
func NewRunMetrics(name string, opts ...RunMetricsOption) *RunMetrics {
	m := &RunMetrics{
		name: name,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// WithAPIStats sets the API metrics provider.
func WithAPIStats(provider APIMetricsProvider) RunMetricsOption {
	return func(m *RunMetrics) {
		m.apiStats = provider
	}
}

// WithStorageStats sets the storage stats function.
func WithStorageStats(fn func() model.StorageStats) RunMetricsOption {
	return func(m *RunMetrics) {
		m.storageStats = fn
	}
}

// Start marks the beginning of a processor run and captures the storage baseline.
func (m *RunMetrics) Start() {
	if m.storageStats != nil {
		m.storageBaseline = m.storageStats()
	}
	m.startTime = time.Now()
}

// Finish marks the end of a processor run.
func (m *RunMetrics) Finish() {
	m.endTime = time.Now()
}

// Duration returns the elapsed time. If Finish() hasn't been called,
// returns time since Start().
func (m *RunMetrics) Duration() time.Duration {
	if m.endTime.IsZero() {
		return time.Since(m.startTime)
	}
	return m.endTime.Sub(m.startTime)
}

// StorageStatsDelta returns the difference between current storage stats
// and the baseline captured at Start().
func (m *RunMetrics) StorageStatsDelta() model.StorageStats {
	if m.storageStats == nil {
		return model.StorageStats{}
	}
	current := m.storageStats()
	return model.StorageStats{
		ProcessedItems: current.ProcessedItems - m.storageBaseline.ProcessedItems,
		FailedItems:    current.FailedItems - m.storageBaseline.FailedItems,
		BatchesWritten: current.BatchesWritten - m.storageBaseline.BatchesWritten,
		QueueLength:    current.QueueLength,
	}
}
