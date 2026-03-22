package processors

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sprawler/internal/config"
	"sprawler/internal/logger"
	"sprawler/internal/metrics"
	"sprawler/internal/model"
	"sprawler/internal/throttle"
)

// oneDriveCounters holds all atomic counters for a single OneDrive run.
type oneDriveCounters struct {
	SitesDiscovered atomic.Int64
	SitesDispatched atomic.Int64
	SitesCompleted  atomic.Int64
	Succeeded       atomic.Int64
	Failed          atomic.Int64
	Users           atomic.Int64
	UPSProfiles     atomic.Int64
	LockedSites     atomic.Int64
	OrphanedSites   atomic.Int64
	NoOwnerSites    atomic.Int64
}

// knownLockedStates is the set of site lock states that prevent user enumeration.
var knownLockedStates = map[string]bool{
	"Locked":      true,
	"ReadOnly":    true,
	"NoAccess":    true,
	"NoAdditions": true,
}

// profileInput holds the fields extracted from a site needed to fetch a user profile.
type profileInput struct {
	AccountName      string
	PersonalUrl      string
	LastModifiedTime string
}

// OneDriveProcessor processes OneDrive personal sites.
type OneDriveProcessor struct {
	config        *config.Config
	dataWriter    DataWriter
	apiClient     APIClient
	logger        *logger.Logger
	activeWorkers *atomic.Int64
}

// NewOneDriveProcessor creates a OneDrive processor.
func NewOneDriveProcessor(cfg *config.Config, dataWriter DataWriter, apiClient APIClient) *OneDriveProcessor {
	return &OneDriveProcessor{
		config:        cfg,
		dataWriter:    dataWriter,
		apiClient:     apiClient,
		logger:        logger.NewLogger("OD"),
		activeWorkers: &atomic.Int64{},
	}
}

// Name returns processor identifier.
func (p *OneDriveProcessor) Name() string {
	return "OneDrive"
}

// Health verifies processor dependencies are accessible.
func (p *OneDriveProcessor) Health(ctx context.Context) error {
	if p.apiClient == nil {
		return fmt.Errorf("API client not initialized")
	}

	if err := p.apiClient.HealthCheck(ctx); err != nil {
		return fmt.Errorf("API client health check failed: %w", err)
	}

	if err := p.dataWriter.HealthCheck(); err != nil {
		return fmt.Errorf("storage health check failed: %w", err)
	}

	return nil
}

// Process executes OneDrive processing pipeline.
//
// Pipeline topology:
//
//	enumeratePersonalSites (CSOM paginator)
//	  -> countDispatched -> N workers (processSite) -+-> sites channel    -> StreamSites
//	                                                 +-> users channel    -> StreamUsers
//	                                                 +-> profiles channel -> StreamProfiles
//	All entity channels -> bridgeChannelsToStorage -> DataWriter
//
// Shutdown: channel closes cascade left-to-right. Each stage owns its output
// channel(s) and closes them when done, naturally draining downstream.
func (p *OneDriveProcessor) Process(ctx context.Context) ProcessorResult {
	p.logger.Info("Starting OneDrive processing")

	totalExpected := int64(p.config.OneDrive.ExpectedSites)
	if p.config.Debug.MaxOneDriveSites > 0 {
		debugCap := int64(p.config.Debug.MaxOneDriveSites)
		if totalExpected == 0 || debugCap < totalExpected {
			totalExpected = debugCap
		}
	}

	counters := &oneDriveCounters{}

	m := metrics.NewRunMetrics("OneDrive",
		metrics.WithAPIStats(p.apiClient),
		metrics.WithStorageStats(func() model.StorageStats {
			return p.dataWriter.GetStats()
		}),
	)
	m.Start()
	defer m.Finish()

	processCtx := context.WithoutCancel(ctx)
	oc := &OutcomeCollector{}

	personalSites := p.enumeratePersonalSites(ctx, counters)
	countedSites := p.countDispatched(processCtx, personalSites, counters)

	scaler := p.newThrottleScaler()
	defer scaler.Stop()

	sites, users, profiles := p.startSiteProcessing(processCtx, countedSites, counters, oc, scaler)

	reporterOpts := []metrics.ReporterOption{
		metrics.WithActiveWorkers(p.activeWorkers),
		metrics.WithScaler(func() metrics.ScalerSnapshot {
			return metrics.ScalerSnapshot{
				Current:    scaler.CurrentLimit(),
				Max:        scaler.MaxWorkers(),
				ScaleDowns: scaler.Stats().ScaleDownEvents,
			}
		}),
		metrics.WithSiteProgress(func() metrics.SiteProgressSnapshot {
			dispatched := counters.SitesDispatched.Load()
			completed := counters.SitesCompleted.Load()
			failed := counters.Failed.Load()
			discovered := counters.SitesDiscovered.Load()
			return metrics.SiteProgressSnapshot{
				Completed:  completed,
				Failed:     failed,
				InFlight:   dispatched - completed,
				Discovered: discovered,
				Total:      totalExpected,
			}
		}),
		metrics.WithEntities([]metrics.NamedCounter{
			{Label: "users", Counter: &counters.Users},
			{Label: "profiles", Counter: &counters.UPSProfiles},
		}),
		metrics.WithChannelStats(func() []metrics.ChannelStat {
			return []metrics.ChannelStat{
				{Name: "sites->workers", Len: len(countedSites), Cap: cap(countedSites)},
				{Name: "users->dataWriter", Len: len(users), Cap: cap(users)},
				{Name: "profiles->dataWriter", Len: len(profiles), Cap: cap(profiles)},
			}
		}),
	}
	reporter := metrics.NewProgressReporter(m, p.logger, reporterOpts...)
	reporter.Start(p.config.OneDrive.ProgressReportInterval)
	defer reporter.Stop()

	p.bridgeChannelsToStorage(processCtx, sites, users, profiles).Wait()

	return p.collectResults(m, counters, oc, scaler)
}

func (p *OneDriveProcessor) enumeratePersonalSites(ctx context.Context, counters *oneDriveCounters) <-chan model.Site {
	sites := make(chan model.Site, p.config.OneDrive.CSOMBufferSize)
	go func() {
		defer close(sites)
		onPage := func(n int) { counters.SitesDiscovered.Add(int64(n)) }
		if err := p.apiClient.GetPersonalSites(ctx, sites, p.config.Debug.MaxOneDriveSites, onPage); err != nil {
			if ctx.Err() != nil {
				p.logger.Info("OneDrive site enumeration stopped (interrupt)")
			} else {
				p.logger.Errorf("OneDrive site enumeration failed: %v", err)
			}
		}
	}()
	return sites
}

func (p *OneDriveProcessor) countDispatched(ctx context.Context, in <-chan model.Site, counters *oneDriveCounters) <-chan model.Site {
	out := make(chan model.Site, 1000)
	go func() {
		defer close(out)
		for site := range in {
			counters.SitesDispatched.Add(1)
			select {
			case out <- site:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func (p *OneDriveProcessor) startSiteProcessing(ctx context.Context, in <-chan model.Site, counters *oneDriveCounters, oc *OutcomeCollector, scaler *throttle.ThrottleScaler) (<-chan model.Site, <-chan model.SiteUser, <-chan model.UserProfile) {
	sites := make(chan model.Site, 1000)
	users := make(chan model.SiteUser, 1000)
	profiles := make(chan model.UserProfile, 1000)

	var wg sync.WaitGroup
	p.startSiteProcessingWorkers(ctx, in, sites, users, profiles, &wg, counters, oc, scaler)

	go func() {
		wg.Wait()
		close(sites)
		close(users)
		close(profiles)
	}()

	return sites, users, profiles
}

func (p *OneDriveProcessor) newThrottleScaler() *throttle.ThrottleScaler {
	var opts []throttle.ScalerOption
	if p.config.OneDrive.ThrottleCheckInterval > 0 {
		opts = append(opts, throttle.WithCheckInterval(p.config.OneDrive.ThrottleCheckInterval))
	}
	scaler := throttle.NewThrottleScaler(p.config.OneDrive.UserWorkers, []throttle.BackpressureSignal{
		{Name: "throttle", GetCount: p.apiClient.GetThrottlingCount, Action: throttle.ScaleHalve, CooldownPeriod: p.config.OneDrive.ThrottleRecoveryCooldown},
		{Name: "connreset", GetCount: p.apiClient.GetNetworkErrorCount, Action: throttle.ScaleHalve, CooldownPeriod: p.config.OneDrive.ThrottleRecoveryCooldown},
	}, p.logger, opts...)
	scaler.Start()
	return scaler
}

func (p *OneDriveProcessor) collectResults(m *metrics.RunMetrics, counters *oneDriveCounters, oc *OutcomeCollector, scaler *throttle.ThrottleScaler) ProcessorResult {
	apiMetrics := p.apiClient.GetMetrics()
	logAPIDiagnostics(p.logger, apiMetrics)
	logTransportDiagnostics(p.logger, p.apiClient.GetTransportStats())

	succeeded := counters.Succeeded.Load()
	failed := counters.Failed.Load()

	result := ProcessorResult{
		ProcessorName:    "OneDrive",
		RecordsExtracted: succeeded,
		RecordsFailed:    failed,
		Duration:         m.Duration(),
		Success:          true,
	}

	if failed > 0 {
		result.Error = fmt.Errorf("OneDrive processing completed with %d failures", failed)
		result.ErrorMsg = result.Error.Error()
	}

	result.ODOutcomes = oc.ODOutcomes()
	result.Summary = p.buildSummary(counters, m, oc, scaler)

	if len(result.ODOutcomes) > 0 {
		if err := p.dataWriter.SaveOneDriveOutcomes(result.ODOutcomes); err != nil {
			p.logger.Errorf("Failed to persist OD site outcomes: %v", err)
		}
	}

	return result
}

// startSiteProcessingWorkers starts site processing worker pools.
func (p *OneDriveProcessor) startSiteProcessingWorkers(ctx context.Context, inputSites <-chan model.Site, outputSites chan<- model.Site, users chan<- model.SiteUser, profiles chan<- model.UserProfile, wg *sync.WaitGroup, counters *oneDriveCounters, oc *OutcomeCollector, scaler *throttle.ThrottleScaler) {
	for i := 0; i < p.config.OneDrive.UserWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for site := range inputSites {
				if !scaler.Acquire(ctx) {
					return
				}
				p.activeWorkers.Add(1)
				p.processSite(ctx, site, outputSites, users, profiles, workerID, counters, oc)
				p.activeWorkers.Add(-1)
				scaler.Release()
			}
		}(i)
	}
}

// processSite handles all sub-operations for a single OneDrive site.
func (p *OneDriveProcessor) processSite(ctx context.Context, site model.Site, outputSites chan<- model.Site, users chan<- model.SiteUser, profiles chan<- model.UserProfile, workerID int, counters *oneDriveCounters, oc *OutcomeCollector) {
	siteStart := time.Now()
	p.logger.Tracef("Worker %d: Processing site %s", workerID, site.SiteUrl)

	select {
	case outputSites <- site:
	case <-ctx.Done():
		return
	}

	var usersFailure *model.OperationFailure
	var profileFailure *model.OperationFailure
	usersFetched := 0
	profileFetched := false

	if !p.shouldSkipLockedSite(site.LockState, workerID, site.SiteUrl, counters) {
		userCtx, userCancel := context.WithTimeout(ctx, p.config.OneDrive.UserFetchTimeout)
		siteUsers, err := p.apiClient.GetSiteUsers(userCtx, site)
		userCancel()

		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				p.logger.Warnf("Worker %d: GetSiteUsers timed out after %v for site %s", workerID, p.config.OneDrive.UserFetchTimeout, site.SiteUrl)
			}
			usersFailure = newOperationFailure(err)
			p.logger.Tracef("Worker %d: Failed to fetch users for site %s: %v", workerID, site.SiteUrl, err)
		} else {
			usersFetched = len(siteUsers)
			for _, user := range siteUsers {
				select {
				case users <- user:
					counters.Users.Add(1)
				case <-ctx.Done():
					return
				}
			}
		}
	}

	profileData := p.buildProfileDataFromSite(site)

	profile, err := p.fetchUserProfile(ctx, profileData, workerID, counters)
	if err != nil {
		p.logger.Errorf("Worker %d: Unexpected error fetching profile for site %s: %v", workerID, site.SiteUrl, err)
		profileFailure = newOperationFailure(err)
	}

	if profile != nil {
		profileFetched = true
		select {
		case profiles <- *profile:
			counters.UPSProfiles.Add(1)
			p.logger.Tracef("Worker %d: Sent profile for URL %s (site %s)", workerID, profile.PersonalUrl, site.SiteId)
		case <-ctx.Done():
			return
		}
	} else {
		p.logger.Tracef("Worker %d: No profile created for site %s", workerID, site.SiteUrl)
	}

	// Counters are mutually exclusive: success only when all sub-operations succeed
	counters.SitesCompleted.Add(1)
	if usersFailure != nil || profileFailure != nil {
		oc.RecordODOutcome(model.ODSiteOutcome{
			SiteURL:        site.SiteUrl,
			SiteID:         site.SiteId,
			UserAccount:    profileData.AccountName,
			Timestamp:      time.Now(),
			Duration:       time.Since(siteStart),
			UsersFailure:   usersFailure,
			ProfileFailure: profileFailure,
			UsersFetched:   usersFetched,
			ProfileFetched: profileFetched,
		})
		counters.Failed.Add(1)
	} else {
		counters.Succeeded.Add(1)
	}
}

// bridgeChannelsToStorage drains entity channels into the data writer.
func (p *OneDriveProcessor) bridgeChannelsToStorage(ctx context.Context, sites <-chan model.Site, users <-chan model.SiteUser, profiles <-chan model.UserProfile) *sync.WaitGroup {
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		p.dataWriter.StreamSites(ctx, sites)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.dataWriter.StreamUsers(ctx, users)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.dataWriter.StreamProfiles(ctx, profiles)
	}()

	return &wg
}

// buildProfileDataFromSite builds profile data from model.Site.
func (p *OneDriveProcessor) buildProfileDataFromSite(site model.Site) profileInput {
	var accountName string
	if site.CreatedByEmail != "" {
		accountName = fmt.Sprintf("i:0#.f|membership|%s", site.CreatedByEmail)
	}

	return profileInput{
		AccountName:      accountName,
		PersonalUrl:      site.SiteUrl,
		LastModifiedTime: site.LastActivityOn,
	}
}

// fetchUserProfile fetches user profile with fallback to site data.
func (p *OneDriveProcessor) fetchUserProfile(ctx context.Context, input profileInput, workerID int, counters *oneDriveCounters) (*model.UserProfile, error) {
	if input.AccountName == "" {
		p.logger.Tracef("Worker %d: Site has no owner, skipping profile creation: %s", workerID, input.PersonalUrl)
		counters.NoOwnerSites.Add(1)
		return nil, nil
	}

	p.logger.Tracef("Worker %d: Fetching profile for user: %s", workerID, input.AccountName)
	profileCtx, profileCancel := context.WithTimeout(ctx, p.config.OneDrive.ProfileFetchTimeout)
	profile, err := p.apiClient.GetUserProfile(profileCtx, input.AccountName)
	profileCancel()

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			p.logger.Warnf("Worker %d: GetUserProfile timed out after %v for user %s", workerID, p.config.OneDrive.ProfileFetchTimeout, input.AccountName)
		}
		p.logger.Tracef("Worker %d: Failed to fetch profile for site %s, skipping profile creation: %v", workerID, input.PersonalUrl, err)
		return nil, err
	}

	if profile.PersonalUrl == "" || profile.AadObjectId == "" {
		p.logger.Debugf("Worker %d: Orphaned personal site - original owner profile deleted for site %s - PersonalUrl:'%s', AadObjectId:'%s'",
			workerID, input.PersonalUrl, profile.PersonalUrl, profile.AadObjectId)
		counters.OrphanedSites.Add(1)
		return nil, nil
	}

	profile.PersonalUrl = p.normalizePersonalUrl(input.PersonalUrl)
	return profile, nil
}

// shouldSkipLockedSite checks if user enumeration should be skipped due to lock state.
func (p *OneDriveProcessor) shouldSkipLockedSite(lockState string, workerID int, siteURL string, counters *oneDriveCounters) bool {
	if knownLockedStates[lockState] {
		counters.LockedSites.Add(1)
		p.logger.Tracef("Worker %d: Skipping user enumeration for locked OneDrive site %s (LockState: '%s')", workerID, siteURL, lockState)
		return true
	}

	return false
}

// normalizePersonalUrl removes trailing slash for consistency.
func (p *OneDriveProcessor) normalizePersonalUrl(url string) string {
	if url != "" && strings.HasSuffix(url, "/") {
		return strings.TrimSuffix(url, "/")
	}
	return url
}

// buildSummary generates a human-readable summary. Zero-value sections are omitted.
func (p *OneDriveProcessor) buildSummary(counters *oneDriveCounters, m *metrics.RunMetrics, oc *OutcomeCollector, scaler *throttle.ThrottleScaler) string {
	sitesCompleted := counters.SitesCompleted.Load()
	upsProfiles := counters.UPSProfiles.Load()
	orphanedSites := counters.OrphanedSites.Load()
	usersCount := counters.Users.Load()
	failed := counters.Failed.Load()

	var sections []string

	// Sites
	siteParts := fmt.Sprintf("%d profiles, %d orphaned", upsProfiles, orphanedSites)
	if noOwner := counters.NoOwnerSites.Load(); noOwner > 0 {
		siteParts += fmt.Sprintf(", %d no-owner", noOwner)
	}
	if locked := counters.LockedSites.Load(); locked > 0 {
		siteParts += fmt.Sprintf(", %d locked", locked)
	}
	sections = append(sections, fmt.Sprintf("%d sites (%s)", sitesCompleted, siteParts))

	// Entities
	sections = append(sections, formatCompact(usersCount)+" users")

	// Failures (only when non-zero)
	if failed > 0 {
		sections = append(sections, fmt.Sprintf("%d failed", failed))
	}

	// Duration
	sections = append(sections, formatDuration(m.Duration()))

	// Scaler (only when interesting)
	if stats := scaler.Stats(); stats.ScaleDownEvents > 0 {
		sections = append(sections, fmt.Sprintf("Scaler: %d/%d workers (%d down, %d up, min %d, %s reduced)",
			scaler.CurrentLimit(), scaler.MaxWorkers(),
			stats.ScaleDownEvents, stats.ScaleUpEvents, stats.MinLimit, formatDuration(stats.TimeReduced)))
	}

	// Failure breakdown
	failParts := formatFailureBreakdown(oc)
	if len(failParts) > 0 {
		sections = append(sections, fmt.Sprintf("Failures: %s", strings.Join(failParts, ", ")))
	}

	return strings.Join(sections, " | ")
}
