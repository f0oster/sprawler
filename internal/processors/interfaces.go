// Package processors defines interfaces for SharePoint and OneDrive data extraction.
package processors

import (
	"context"
	"time"

	"sprawler/internal/api"
	"sprawler/internal/model"
)

// APIClient provides SharePoint and OneDrive site, user, group, and profile data.
type APIClient interface {
	GetSites(ctx context.Context, sites chan<- model.Site, pageSize int, maxPages int, onPage func(int)) error
	GetSiteUsers(ctx context.Context, site model.Site) ([]model.SiteUser, error)
	GetSiteGroups(ctx context.Context, site model.Site, memberTimeout time.Duration) (*api.GroupFetchResult, error)
	GetPersonalSites(ctx context.Context, sites chan<- model.Site, maxSites int, onPage func(int)) error
	GetUserProfile(ctx context.Context, userID string) (*model.UserProfile, error)
	GetMetrics() model.APIMetrics
	GetTransportStats() api.TransportStats
	GetThrottlingCount() int64
	GetNetworkErrorCount() int64
	HealthCheck(ctx context.Context) error
}

// Processor defines the interface for data source processors.
type Processor interface {
	Name() string
	Process(ctx context.Context) ProcessorResult
	Health(ctx context.Context) error
}

// DataWriter consumes streamed entities and records processing outcomes.
type DataWriter interface {
	StreamSites(ctx context.Context, sites <-chan model.Site)
	StreamUsers(ctx context.Context, users <-chan model.SiteUser)
	StreamProfiles(ctx context.Context, profiles <-chan model.UserProfile)
	StreamGroups(ctx context.Context, groups <-chan model.SiteGroup)
	StreamMembers(ctx context.Context, members <-chan model.GroupMember)

	SaveSiteOutcomes(outcomes []model.SPSiteOutcome) error
	SaveOneDriveOutcomes(outcomes []model.ODSiteOutcome) error

	GetStats() model.StorageStats
	HealthCheck() error
}

// ProcessorResult contains processor execution results.
type ProcessorResult struct {
	ProcessorName    string
	RecordsExtracted int64
	RecordsFailed    int64
	Duration         time.Duration

	SPOutcomes []model.SPSiteOutcome
	ODOutcomes []model.ODSiteOutcome

	Success  bool
	Error    error
	ErrorMsg string
	Summary  string
}
