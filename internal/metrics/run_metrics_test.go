package metrics

import (
	"sync/atomic"
	"testing"
	"time"

	"sprawler/internal/model"
)

func TestRunMetrics_Lifecycle(t *testing.T) {
	m := NewRunMetrics("test")
	m.Start()
	time.Sleep(10 * time.Millisecond)

	// Duration should be non-zero before Finish
	if d := m.Duration(); d <= 0 {
		t.Errorf("Duration before Finish = %v, want > 0", d)
	}

	m.Finish()

	// Duration should be stable after Finish
	d1 := m.Duration()
	time.Sleep(10 * time.Millisecond)
	d2 := m.Duration()
	if d1 != d2 {
		t.Errorf("Duration changed after Finish: %v -> %v", d1, d2)
	}
}

func TestRunMetrics_StorageStatsDelta(t *testing.T) {
	processedItems := int64(100)
	failedItems := int64(5)

	m := NewRunMetrics("test",
		WithStorageStats(func() model.StorageStats {
			return model.StorageStats{
				ProcessedItems: atomic.LoadInt64(&processedItems),
				FailedItems:    atomic.LoadInt64(&failedItems),
			}
		}),
	)

	// Baseline captured at Start(): processed=100, failed=5
	m.Start()

	// Simulate some writes happening
	atomic.AddInt64(&processedItems, 50)
	atomic.AddInt64(&failedItems, 2)

	delta := m.StorageStatsDelta()
	if delta.ProcessedItems != 50 {
		t.Errorf("StorageStatsDelta.ProcessedItems = %d, want 50", delta.ProcessedItems)
	}
	if delta.FailedItems != 2 {
		t.Errorf("StorageStatsDelta.FailedItems = %d, want 2", delta.FailedItems)
	}
}

func TestRunMetrics_StorageStatsDelta_NilFunc(t *testing.T) {
	m := NewRunMetrics("test")

	delta := m.StorageStatsDelta()
	if delta.ProcessedItems != 0 || delta.FailedItems != 0 {
		t.Errorf("StorageStatsDelta with nil func = %+v, want zero", delta)
	}
}
