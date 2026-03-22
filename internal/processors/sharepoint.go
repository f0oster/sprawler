package processors

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

// sharePointCounters holds all atomic counters for a single SharePoint run.
type sharePointCounters struct {
	SitesDiscovered atomic.Int64
	SitesDispatched atomic.Int64
	SitesCompleted  atomic.Int64
	Succeeded       atomic.Int64
	Failed          atomic.Int64
	Users           atomic.Int64
	Groups          atomic.Int64
	Members         atomic.Int64

	// Skip counters
	SkipTemplate   atomic.Int64
	SkipTenantRoot atomic.Int64
}

// spSiteTracker collects results from concurrent user/group workers for a single site.
type spSiteTracker struct {
	site      model.Site
	startedAt time.Time
	remaining atomic.Int32
	// Written by user worker
	usersFailure atomic.Pointer[model.OperationFailure]
	usersFetched atomic.Int32
	// Written by group worker
	groupsFailure       atomic.Pointer[model.OperationFailure]
	groupsFetched       atomic.Int32
	membersFetched      atomic.Int32
	memberErrors        atomic.Int32
	memberTimeoutErrors atomic.Int32
}

// finalizeSiteOutcome is called by each worker when it finishes. When the last
// worker finishes (remaining hits 0), it builds an SPSiteOutcome if any operation
// failed and records success/failure.
func finalizeSiteOutcome(outcomes *sync.Map, tracker *spSiteTracker, counters *sharePointCounters, oc *OutcomeCollector) {
	if tracker.remaining.Add(-1) != 0 {
		return
	}
	outcomes.Delete(tracker.site.SiteUrl)
	counters.SitesCompleted.Add(1)

	usersFailure := tracker.usersFailure.Load()
	groupsFailure := tracker.groupsFailure.Load()
	memberErrs := tracker.memberErrors.Load()
	memberTimeoutErrs := tracker.memberTimeoutErrors.Load()

	hasFailed := usersFailure != nil || groupsFailure != nil || memberErrs > 0 || memberTimeoutErrs > 0
	if hasFailed {
		oc.RecordSPOutcome(model.SPSiteOutcome{
			SiteURL:             tracker.site.SiteUrl,
			SiteID:              tracker.site.SiteId,
			Timestamp:           time.Now(),
			Duration:            time.Since(tracker.startedAt),
			UsersFailure:        usersFailure,
			GroupsFailure:       groupsFailure,
			UsersFetched:        int(tracker.usersFetched.Load()),
			GroupsFetched:       int(tracker.groupsFetched.Load()),
			MembersFetched:      int(tracker.membersFetched.Load()),
			MemberErrors:        int(memberErrs),
			MemberTimeoutErrors: int(memberTimeoutErrs),
		})
		counters.Failed.Add(1)
	} else {
		counters.Succeeded.Add(1)
	}
}

// SharePointProcessor processes SharePoint sites using concurrent workers.
type SharePointProcessor struct {
	config        *config.Config
	dataWriter    DataWriter
	apiClient     APIClient
	logger        *logger.Logger
	activeWorkers *atomic.Int64
}

// NewSharePointProcessor creates a SharePoint processor with concurrent processing.
func NewSharePointProcessor(cfg *config.Config, dataWriter DataWriter, apiClient APIClient) *SharePointProcessor {
	return &SharePointProcessor{
		config:        cfg,
		dataWriter:    dataWriter,
		apiClient:     apiClient,
		logger:        logger.NewLogger("SP"),
		activeWorkers: &atomic.Int64{},
	}
}

// Name returns processor identifier.
func (p *SharePointProcessor) Name() string {
	return "SharePoint"
}

// Health verifies processor dependencies are accessible.
func (p *SharePointProcessor) Health(ctx context.Context) error {
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

// Process executes SharePoint site processing pipeline.
//
// Pipeline topology:
//
//	enumerateSites (paginator)
//	  -> startFiltering -> startDispatching -+-> userSites  -> N user workers  --> users channel
//	                                         +-> groupSites -> M group workers -+-> groups channel
//	                                                                            +-> members channel
//	All entity channels -> bridgeChannelsToStorage -> DataWriter
//
// Shutdown: channel closes cascade left-to-right. Each stage owns its output
// channel(s) and closes them when done, naturally draining downstream.
func (p *SharePointProcessor) Process(ctx context.Context) ProcessorResult {
	p.logger.Info("Starting SharePoint processing")

	totalExpected := int64(p.config.SharePoint.ExpectedSites)
	if p.config.Debug.MaxPages > 0 {
		debugCap := int64(p.config.Debug.MaxPages) * int64(p.config.SharePoint.PageSize)
		if totalExpected == 0 || debugCap < totalExpected {
			totalExpected = debugCap
		}
	}

	counters := &sharePointCounters{}

	m := metrics.NewRunMetrics("SharePoint",
		metrics.WithAPIStats(p.apiClient),
		metrics.WithStorageStats(func() model.StorageStats {
			return p.dataWriter.GetStats()
		}),
	)
	m.Start()
	defer m.Finish()

	processCtx := context.WithoutCancel(ctx)
	var outcomes sync.Map
	oc := &OutcomeCollector{}

	rawSites := p.enumerateSites(ctx, counters)
	filteredSites := p.startFiltering(processCtx, rawSites, counters)
	sites, userSites, groupSites := p.startDispatching(processCtx, filteredSites, counters, &outcomes)

	scaler := p.newThrottleScaler()
	defer scaler.Stop()

	users, groups, members := p.startExtraction(processCtx, userSites, groupSites, counters, &outcomes, oc, scaler)

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
			{Label: "groups", Counter: &counters.Groups},
			{Label: "members", Counter: &counters.Members},
		}),
		metrics.WithSkips([]metrics.NamedCounter{
			{Label: "template", Counter: &counters.SkipTemplate},
			{Label: "tenant_root", Counter: &counters.SkipTenantRoot},
		}),
		metrics.WithChannelStats(func() []metrics.ChannelStat {
			stats := []metrics.ChannelStat{
				{Name: "sites->users", Len: len(userSites), Cap: cap(userSites)},
				{Name: "users->dataWriter", Len: len(users), Cap: cap(users)},
			}
			if groupSites != nil {
				stats = append(stats,
					metrics.ChannelStat{Name: "sites->groups", Len: len(groupSites), Cap: cap(groupSites)},
					metrics.ChannelStat{Name: "groups->dataWriter", Len: len(groups), Cap: cap(groups)},
					metrics.ChannelStat{Name: "members->dataWriter", Len: len(members), Cap: cap(members)},
				)
			}
			return stats
		}),
	}
	reporter := metrics.NewProgressReporter(m, p.logger, reporterOpts...)
	reporter.Start(p.config.SharePoint.ProgressReportInterval)
	defer reporter.Stop()

	p.bridgeChannelsToStorage(processCtx, sites, users, groups, members).Wait()

	return p.collectResults(m, counters, oc, scaler)
}

func (p *SharePointProcessor) enumerateSites(ctx context.Context, counters *sharePointCounters) <-chan model.Site {
	sites := make(chan model.Site, p.config.SharePoint.SiteEnumBufferSize)
	go func() {
		defer close(sites)
		onPage := func(n int) { counters.SitesDiscovered.Add(int64(n)) }
		if err := p.apiClient.GetSites(ctx, sites, p.config.SharePoint.PageSize, p.config.Debug.MaxPages, onPage); err != nil {
			if ctx.Err() != nil {
				p.logger.Info("Site enumeration stopped (interrupt)")
			} else {
				p.logger.Errorf("Site enumeration failed: %v", err)
			}
		}
	}()
	return sites
}

func (p *SharePointProcessor) startFiltering(ctx context.Context, in <-chan model.Site, counters *sharePointCounters) <-chan model.Site {
	out := make(chan model.Site, 400)
	go p.filterSites(ctx, in, out, counters)
	return out
}

func (p *SharePointProcessor) startDispatching(ctx context.Context, in <-chan model.Site, counters *sharePointCounters, outcomes *sync.Map) (sites, userSites, groupSites <-chan model.Site) {
	sitesOut := make(chan model.Site, 1000)
	userSitesOut := make(chan model.Site, 400)
	var groupSitesOut chan model.Site
	if p.config.SharePoint.ProcessGroups {
		groupSitesOut = make(chan model.Site, 200)
	}
	go p.dispatchSitesToWorkers(ctx, in, sitesOut, userSitesOut, groupSitesOut, counters, outcomes)
	return sitesOut, userSitesOut, groupSitesOut
}

func (p *SharePointProcessor) startExtraction(ctx context.Context, userSites, groupSites <-chan model.Site, counters *sharePointCounters, outcomes *sync.Map, oc *OutcomeCollector, scaler *throttle.ThrottleScaler) (<-chan model.SiteUser, <-chan model.SiteGroup, <-chan model.GroupMember) {
	users := make(chan model.SiteUser, 1000)
	groups := make(chan model.SiteGroup, 500)
	members := make(chan model.GroupMember, 1000)

	var wg sync.WaitGroup
	p.startDataExtractionWorkers(ctx, userSites, groupSites, users, groups, members, &wg, counters, outcomes, oc, scaler)

	go func() {
		wg.Wait()
		close(users)
		close(groups)
		close(members)
	}()

	return users, groups, members
}

func (p *SharePointProcessor) newThrottleScaler() *throttle.ThrottleScaler {
	totalWorkers := p.config.SharePoint.UserWorkers
	if p.config.SharePoint.ProcessGroups {
		totalWorkers += p.config.SharePoint.GroupWorkers
	}
	var opts []throttle.ScalerOption
	if p.config.SharePoint.ThrottleCheckInterval > 0 {
		opts = append(opts, throttle.WithCheckInterval(p.config.SharePoint.ThrottleCheckInterval))
	}
	scaler := throttle.NewThrottleScaler(totalWorkers, []throttle.BackpressureSignal{
		{Name: "throttle", GetCount: p.apiClient.GetThrottlingCount, Action: throttle.ScaleHalve, CooldownPeriod: p.config.SharePoint.ThrottleRecoveryCooldown},
		{Name: "connreset", GetCount: p.apiClient.GetNetworkErrorCount, Action: throttle.ScaleHalve, CooldownPeriod: p.config.SharePoint.ThrottleRecoveryCooldown},
	}, p.logger, opts...)
	scaler.Start()
	return scaler
}

func (p *SharePointProcessor) collectResults(m *metrics.RunMetrics, counters *sharePointCounters, oc *OutcomeCollector, scaler *throttle.ThrottleScaler) ProcessorResult {
	apiMetrics := p.apiClient.GetMetrics()
	logAPIDiagnostics(p.logger, apiMetrics)
	logTransportDiagnostics(p.logger, p.apiClient.GetTransportStats())

	succeeded := counters.Succeeded.Load()
	failed := counters.Failed.Load()

	result := ProcessorResult{
		ProcessorName:    "SharePoint",
		RecordsExtracted: succeeded,
		RecordsFailed:    failed,
		Duration:         m.Duration(),
		Success:          true,
	}

	if failed > 0 {
		result.Error = fmt.Errorf("SharePoint processing completed with %d failures", failed)
		result.ErrorMsg = result.Error.Error()
	}

	result.SPOutcomes = oc.SPOutcomes()
	result.Summary = p.buildSummary(counters, m, oc, scaler)

	if len(result.SPOutcomes) > 0 {
		if err := p.dataWriter.SaveSiteOutcomes(result.SPOutcomes); err != nil {
			p.logger.Errorf("Failed to persist SP site outcomes: %v", err)
		}
	}

	return result
}

// filterSites filters sites based on business rules.
func (p *SharePointProcessor) filterSites(ctx context.Context, rawSites <-chan model.Site, filteredSites chan<- model.Site, counters *sharePointCounters) {
	defer close(filteredSites)

	skipTemplates := buildTemplateSkipSet(p.config.SharePoint.SkipTemplates)

	for {
		select {
		case site, ok := <-rawSites:
			if !ok {
				p.logger.Info("Site filtering complete")
				return
			}

			if skipReason := shouldSkipSite(site, skipTemplates); skipReason != "" {
				switch skipReason {
				case SkipReasonTemplate:
					counters.SkipTemplate.Add(1)
				case SkipReasonTenantRoot:
					counters.SkipTenantRoot.Add(1)
				}
				continue
			}

			select {
			case filteredSites <- site:
			case <-ctx.Done():
				return
			}

		case <-ctx.Done():
			return
		}
	}
}

// dispatchSitesToWorkers streams site metadata to the data writer and dispatches to extraction workers.
func (p *SharePointProcessor) dispatchSitesToWorkers(ctx context.Context, inputSites <-chan model.Site, sites, userSites, groupSites chan<- model.Site, counters *sharePointCounters, outcomes *sync.Map) {
	defer func() {
		p.logger.Debug("Site distribution shutting down")
		close(sites)
		close(userSites)
		if groupSites != nil {
			close(groupSites)
		}
	}()

	workersPerSite := int32(1)
	if p.config.SharePoint.ProcessGroups && groupSites != nil {
		workersPerSite = 2
	}

	for {
		select {
		case site, ok := <-inputSites:
			if !ok {
				p.logger.Debugf("Site distribution complete: %d sites processed", counters.SitesDispatched.Load())
				return
			}

			counters.SitesDispatched.Add(1)

			tracker := &spSiteTracker{site: site, startedAt: time.Now()}
			tracker.remaining.Store(workersPerSite)
			outcomes.Store(site.SiteUrl, tracker)

			if dispatched := counters.SitesDispatched.Load(); dispatched%100 == 0 {
				p.logger.Debugf("Distributed %d sites to workers", dispatched)
			}

			select {
			case sites <- site:
			case <-ctx.Done():
				return
			}

			select {
			case userSites <- site:
			case <-ctx.Done():
				return
			}

			if p.config.SharePoint.ProcessGroups && groupSites != nil {
				select {
				case groupSites <- site:
				case <-ctx.Done():
					return
				}
			}

		case <-ctx.Done():
			return
		}
	}
}

// startDataExtractionWorkers starts user and group data extraction worker pools.
func (p *SharePointProcessor) startDataExtractionWorkers(ctx context.Context, userSites, groupSites <-chan model.Site, users chan<- model.SiteUser, groups chan<- model.SiteGroup, members chan<- model.GroupMember, wg *sync.WaitGroup, counters *sharePointCounters, outcomes *sync.Map, oc *OutcomeCollector, scaler *throttle.ThrottleScaler) {
	for i := 0; i < p.config.SharePoint.UserWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for site := range userSites {
				if !scaler.Acquire(ctx) {
					return
				}
				p.activeWorkers.Add(1)
				p.processSiteUsers(ctx, site, users, workerID, counters, outcomes, oc)
				p.activeWorkers.Add(-1)
				scaler.Release()
			}
		}(i)
	}

	if p.config.SharePoint.ProcessGroups {
		for i := 0; i < p.config.SharePoint.GroupWorkers; i++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				for site := range groupSites {
					if !scaler.Acquire(ctx) {
						return
					}
					p.activeWorkers.Add(1)
					p.processSiteGroups(ctx, site, groups, members, workerID, counters, outcomes, oc)
					p.activeWorkers.Add(-1)
					scaler.Release()
				}
			}(i)
		}
	}
}

// processSiteUsers extracts users from a site.
func (p *SharePointProcessor) processSiteUsers(ctx context.Context, site model.Site, users chan<- model.SiteUser, workerID int, counters *sharePointCounters, outcomes *sync.Map, oc *OutcomeCollector) {
	val, ok := outcomes.Load(site.SiteUrl)
	if !ok {
		p.logger.Errorf("Worker %d: tracker not found for site %s, skipping", workerID, site.SiteUrl)
		counters.SitesCompleted.Add(1)
		counters.Failed.Add(1)
		return
	}
	tracker := val.(*spSiteTracker)

	apiCtx, apiCancel := context.WithTimeout(ctx, p.config.SharePoint.UserFetchTimeout)
	siteUsers, err := p.apiClient.GetSiteUsers(apiCtx, site)
	apiCancel()

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			p.logger.Warnf("Worker %d: GetSiteUsers timed out after %v for site %s", workerID, p.config.SharePoint.UserFetchTimeout, site.SiteUrl)
		}
		tracker.usersFailure.Store(newOperationFailure(err))
		p.logger.Debugf("Worker %d: Failed to fetch users for site %s: %v", workerID, site.SiteUrl, err)
		finalizeSiteOutcome(outcomes, tracker, counters, oc)
		return
	}

	for _, user := range siteUsers {
		select {
		case users <- user:
		case <-ctx.Done():
			return
		}
	}

	tracker.usersFetched.Store(int32(len(siteUsers)))
	counters.Users.Add(int64(len(siteUsers)))
	finalizeSiteOutcome(outcomes, tracker, counters, oc)
}

// processSiteGroups extracts groups and members from a site.
func (p *SharePointProcessor) processSiteGroups(ctx context.Context, site model.Site, groups chan<- model.SiteGroup, members chan<- model.GroupMember, workerID int, counters *sharePointCounters, outcomes *sync.Map, oc *OutcomeCollector) {
	val, ok := outcomes.Load(site.SiteUrl)
	if !ok {
		p.logger.Errorf("Worker %d: tracker not found for site %s, skipping", workerID, site.SiteUrl)
		counters.SitesCompleted.Add(1)
		counters.Failed.Add(1)
		return
	}
	tracker := val.(*spSiteTracker)

	apiCtx, apiCancel := context.WithTimeout(ctx, p.config.SharePoint.GroupFetchTimeout)
	result, err := p.apiClient.GetSiteGroups(apiCtx, site, p.config.SharePoint.MemberFetchTimeout)
	apiCancel()

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			p.logger.Warnf("Worker %d: GetSiteGroups timed out after %v for site %s", workerID, p.config.SharePoint.GroupFetchTimeout, site.SiteUrl)
		}
		tracker.groupsFailure.Store(newOperationFailure(err))
		p.logger.Debugf("Worker %d: Failed to fetch groups for site %s: %v", workerID, site.SiteUrl, err)
		finalizeSiteOutcome(outcomes, tracker, counters, oc)
		return
	}

	// Record per-group member fetch failures on the tracker
	if result.MemberErrors > 0 {
		tracker.memberErrors.Store(int32(result.MemberErrors))
	}
	if result.MemberTimeoutErrors > 0 {
		tracker.memberTimeoutErrors.Store(int32(result.MemberTimeoutErrors))
	}

	for _, group := range result.Groups {
		select {
		case groups <- group:
		case <-ctx.Done():
			return
		}
	}

	if p.config.SharePoint.ProcessGroupMembers {
		for _, member := range result.Members {
			select {
			case members <- member:
			case <-ctx.Done():
				return
			}
		}
		tracker.membersFetched.Store(int32(len(result.Members)))
		counters.Members.Add(int64(len(result.Members)))
	}

	tracker.groupsFetched.Store(int32(len(result.Groups)))
	counters.Groups.Add(int64(len(result.Groups)))
	finalizeSiteOutcome(outcomes, tracker, counters, oc)
}

// bridgeChannelsToStorage drains entity channels into the data writer.
func (p *SharePointProcessor) bridgeChannelsToStorage(ctx context.Context, sites <-chan model.Site, users <-chan model.SiteUser, groups <-chan model.SiteGroup, members <-chan model.GroupMember) *sync.WaitGroup {
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

	if p.config.SharePoint.ProcessGroups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.dataWriter.StreamGroups(ctx, groups)
		}()

		if p.config.SharePoint.ProcessGroupMembers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p.dataWriter.StreamMembers(ctx, members)
			}()
		}
	}

	return &wg
}

// buildSummary generates a human-readable summary. Zero-value sections are omitted.
func (p *SharePointProcessor) buildSummary(counters *sharePointCounters, m *metrics.RunMetrics, oc *OutcomeCollector, scaler *throttle.ThrottleScaler) string {
	discovered := counters.SitesDiscovered.Load()
	processed := counters.Succeeded.Load()
	failed := counters.Failed.Load()

	var sections []string

	// Sites + skip detail
	if discovered > 0 {
		type skipEntry struct {
			label string
			count int64
		}
		skips := []skipEntry{
			{"template", counters.SkipTemplate.Load()},
			{"tenant_root", counters.SkipTenantRoot.Load()},
		}

		totalSkipped := int64(0)
		var skipParts []string
		for _, s := range skips {
			if s.count > 0 {
				totalSkipped += s.count
				skipParts = append(skipParts, fmt.Sprintf("%d %s", s.count, s.label))
			}
		}
		sort.Strings(skipParts)

		if totalSkipped > 0 {
			sections = append(sections, fmt.Sprintf("%d sites (%d skipped: %s)",
				discovered, totalSkipped, strings.Join(skipParts, ", ")))
		} else {
			sections = append(sections, fmt.Sprintf("%d sites", discovered))
		}
	}

	// Results
	if failed > 0 {
		sections = append(sections, fmt.Sprintf("%d ok / %d failed", processed, failed))
	} else {
		sections = append(sections, fmt.Sprintf("%d ok", processed))
	}

	// Entities
	var entityParts []string
	for _, kv := range []struct {
		count int64
		label string
	}{
		{counters.Users.Load(), "users"},
		{counters.Groups.Load(), "groups"},
		{counters.Members.Load(), "members"},
	} {
		if kv.count > 0 {
			entityParts = append(entityParts, formatCompact(kv.count)+" "+kv.label)
		}
	}
	if len(entityParts) > 0 {
		sections = append(sections, strings.Join(entityParts, ", "))
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
