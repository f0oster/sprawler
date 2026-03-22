package model

// StorageStats provides storage performance metrics.
type StorageStats struct {
	ProcessedItems int64
	FailedItems    int64
	BatchesWritten int64
	QueueLength    int64
}

// APIMetrics provides counters for API performance monitoring.
type APIMetrics struct {
	TotalDuration    int64
	AvgDuration      int64
	StatusCodes      map[int]int64
	TransportRetries int64
}
