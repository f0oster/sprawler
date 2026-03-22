// Package storage provides data persistence for SharePoint enumeration data.
package storage

import (
	"context"
	"time"

	"sprawler/internal/model"
)

// Storage is the backend contract. New backends must implement this.
type Storage interface {
	StreamSites(ctx context.Context, sites <-chan model.Site)
	StreamUsers(ctx context.Context, users <-chan model.SiteUser)
	StreamProfiles(ctx context.Context, profiles <-chan model.UserProfile)
	StreamGroups(ctx context.Context, groups <-chan model.SiteGroup)
	StreamMembers(ctx context.Context, members <-chan model.GroupMember)

	SaveSiteOutcomes(outcomes []model.SPSiteOutcome) error
	SaveOneDriveOutcomes(outcomes []model.ODSiteOutcome) error

	GetStats() model.StorageStats
	HealthCheck() error

	InitializeRunStatus(ctx context.Context) error
	MarkRunCompleted(ctx context.Context) error
	Close() error
}

// Archiver is optionally implemented by backends that support post-run archival.
type Archiver interface {
	ArchiveDatabase() error
}

// Config holds storage configuration.
type Config struct {
	Type             string
	Path             string
	Name             string
	MaxConns         int
	Recreate         bool
	BatchSize        int
	FlushInterval    time.Duration
	QueueCapacity    int
	EnableFailureLog bool
	FailureLogPath   string

	// SQLite tuning
	BusyTimeout int // Lock wait timeout in milliseconds
	CacheSize   int // Page cache size (negative = KB)
}
