package api

import (
	"sync"
	"sync/atomic"
)

// counterMap is a thread-safe map of string keys to atomic int64 counters.
// Used for tracking HTTP status code counts where the key set is dynamic.
type counterMap struct {
	mu       sync.RWMutex
	counters map[string]*atomic.Int64
}

func newCounterMap() *counterMap {
	return &counterMap{
		counters: make(map[string]*atomic.Int64),
	}
}

// Add atomically increments the counter for the given key.
// Creates the counter on first use.
func (cm *counterMap) Add(key string, n int64) {
	cm.mu.RLock()
	if counter, exists := cm.counters[key]; exists {
		cm.mu.RUnlock()
		counter.Add(n)
		return
	}
	cm.mu.RUnlock()

	cm.mu.Lock()
	if counter, exists := cm.counters[key]; exists {
		cm.mu.Unlock()
		counter.Add(n)
		return
	}
	cm.counters[key] = &atomic.Int64{}
	counter := cm.counters[key]
	cm.mu.Unlock()

	counter.Add(n)
}

// Snapshot returns a copy of all counters at the time of the call.
func (cm *counterMap) Snapshot() map[string]int64 {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	result := make(map[string]int64, len(cm.counters))
	for key, counter := range cm.counters {
		result[key] = counter.Load()
	}
	return result
}
