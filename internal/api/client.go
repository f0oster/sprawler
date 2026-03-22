// Package api provides SharePoint/OneDrive API client with metrics and retry handling.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/koltyakov/gosip"
	"github.com/koltyakov/gosip/api"

	csomhelpers "sprawler/internal/api/csom"
	"sprawler/internal/auth/entraid"
	"sprawler/internal/auth/entraid/clientcredential"
	"sprawler/internal/config"
	"sprawler/internal/logger"
	"sprawler/internal/model"
)

// contextKey is an unexported type for context keys to prevent collisions.
type contextKey struct{}

// requestStartTimeKey is the context key for request start time.
var requestStartTimeKey = contextKey{}

const (
	// TenantAdminSiteCollectionsList is the hidden SharePoint list that contains
	// aggregated site collection metadata, accessible via the tenant admin site.
	TenantAdminSiteCollectionsList = "DO_NOT_DELETE_SPLIST_TENANTADMIN_AGGREGATED_SITECOLLECTIONS"

	// SiteMetadataFields is the OData $select projection for site enumeration.
	SiteMetadataFields = "SiteUrl,TimeCreated,Modified,Title,TemplateName,CreatedByEmail,GroupId,SiteId,LastActivityOn,StorageUsed"

	// OneDrivePersonalSitePattern is the URL path segment that identifies OneDrive
	// personal sites in CSOM filter queries (e.g., "Url -like 'tenant-my.sharepoint.com/personal/'").
	OneDrivePersonalSitePattern = "personal/"
)

// TransportStats summarizes [ThrottledTransport] behavior over a run.
type TransportStats struct {
	GateActivations  int64
	TotalGateWait    time.Duration
	TransportRetries int64
}

// ThrottledTransport is an http.RoundTripper that gates all outgoing requests
// behind a shared backoff window when a 429/503 is received. This prevents the
// thundering-herd problem where many workers independently discover throttling.
//
// ThrottledTransport is safe for concurrent use.
type ThrottledTransport struct {
	base          http.RoundTripper
	mu            sync.Mutex
	pauseUntil    time.Time
	logger        *logger.Logger
	maxRetries    int
	retryBackoffs []time.Duration
	throttlePause time.Duration

	gateActivations  atomic.Int64
	totalGateWait    atomic.Int64 // nanoseconds
	transportRetries atomic.Int64
}

// RoundTrip implements http.RoundTripper. It gates requests behind a shared
// backoff window, retries transient network errors for idempotent methods,
// and sets the backoff window on 429/503 responses. This runs beneath gosip's
// own retry layer, which handles HTTP-level retries via RetryPolicies.
func (t *ThrottledTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Gate: block all workers if we're in a backoff window from a prior 429/503
	t.mu.Lock()
	wait := time.Until(t.pauseUntil)
	t.mu.Unlock()
	if wait > 0 {
		t.logger.Debugf("Throttle gate: waiting %v before request to %s", wait, req.URL.Host)
		waitStart := time.Now()
		select {
		case <-time.After(wait):
		case <-req.Context().Done():
			t.totalGateWait.Add(time.Since(waitStart).Nanoseconds())
			return nil, req.Context().Err()
		}
		t.totalGateWait.Add(time.Since(waitStart).Nanoseconds())
	}

	// Retry transient network errors (connection reset, EOF, net.OpError) for
	// idempotent methods. This is separate from gosip's HTTP-status retries.
	var resp *http.Response
	var err error
	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		resp, err = t.base.RoundTrip(req)
		if err == nil {
			break
		}
		if !isTransientNetError(err) || !isIdempotent(req.Method) {
			break
		}
		if attempt >= t.maxRetries {
			break
		}
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		t.transportRetries.Add(1)
		backoff := t.retryBackoffs[attempt%len(t.retryBackoffs)]
		t.logger.Warnf("Transport retry %d/%d for %s %s: %v", attempt+1, t.maxRetries, req.Method, req.URL.Host, err)
		select {
		case <-time.After(backoff):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}

	// On 429/503, capture Retry-After and update gate
	if err == nil && (resp.StatusCode == 429 || resp.StatusCode == 503) {
		pauseDuration := t.throttlePause
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if seconds, parseErr := strconv.Atoi(ra); parseErr == nil && seconds > 0 {
				pauseDuration = time.Duration(seconds) * time.Second
			}
		} else {
			t.logger.Warnf("Throttle gate: %d received without Retry-After header, using %v default. Response headers: %v",
				resp.StatusCode, pauseDuration, resp.Header)
		}
		t.mu.Lock()
		newPause := time.Now().Add(pauseDuration)
		if newPause.After(t.pauseUntil) {
			t.pauseUntil = newPause
			t.gateActivations.Add(1)
			t.logger.Infof("Throttle gate: %d received, pausing all requests for %v", resp.StatusCode, pauseDuration)
		}
		t.mu.Unlock()
	}

	return resp, err
}

// TransportStats returns summary statistics for the transport's lifetime.
func (t *ThrottledTransport) TransportStats() TransportStats {
	return TransportStats{
		GateActivations:  t.gateActivations.Load(),
		TotalGateWait:    time.Duration(t.totalGateWait.Load()),
		TransportRetries: t.transportRetries.Load(),
	}
}

type atomicMetrics struct {
	requestCount    atomic.Int64
	networkErrors   atomic.Int64
	throttlingCount atomic.Int64
	totalDuration   atomic.Int64
	statusCodes     *counterMap
}

// Client wraps a gosip SharePoint client with metrics collection, retry
// handling, and throttle-aware transport. It is safe for concurrent use.
type Client struct {
	gosipClient *gosip.SPClient
	authCfg     config.AuthConfig
	retryPolicy map[int]int
	logger      *logger.Logger
	metrics     *atomicMetrics
	transport   *ThrottledTransport
}

// NewClient creates a Client configured with certificate-based authentication.
func NewClient(cfg config.AuthConfig, tcfg config.TransportConfig) (*Client, error) {
	logger := logger.NewLogger("API")
	transport := &ThrottledTransport{
		base:          http.DefaultTransport,
		logger:        logger,
		maxRetries:    tcfg.MaxRetries,
		retryBackoffs: tcfg.RetryBackoffs,
		throttlePause: tcfg.ThrottlePause,
	}

	auth := &clientcredential.AuthCnfg{
		Base:     entraid.Base{SiteURL: cfg.SiteURL, TenantID: cfg.TenantID, ClientID: cfg.ClientID},
		CertPath: cfg.CertPath,
	}
	gosipClient := &gosip.SPClient{AuthCnfg: auth}
	gosipClient.Transport = transport

	client := &Client{
		gosipClient: gosipClient,
		authCfg:     cfg,
		retryPolicy: tcfg.RetryPolicy,
		logger:      logger,
		metrics:     &atomicMetrics{statusCodes: newCounterMap()},
		transport:   transport,
	}

	client.setupHooks()

	return client, nil
}

// setupHooks registers gosip lifecycle hooks for metrics collection.
//
// gosip hook lifecycle:
//   - OnRequest:  called for every attempt (including retries)
//   - OnResponse: called once for the final response (2xx and non-2xx)
//   - OnError:    called once for the final error (non-2xx status or transport error);
//     also called for non-2xx alongside OnResponse
//   - OnRetry:    called before each retry attempt (after OnError for 429s,
//     but without OnError for 500/503/504)
func (c *Client) setupHooks() {
	c.gosipClient.Hooks = &gosip.HookHandlers{
		OnRequest:  c.onRequest,
		OnResponse: c.onResponse,
		OnError:    c.onError,
		OnRetry:    c.onRetry,
	}
}

// onRequest fires on every request attempt. Stamps a start time on the context
// for latency tracking in onResponse.
func (c *Client) onRequest(event *gosip.HookEvent) {
	c.metrics.requestCount.Add(1)
	c.logger.Tracef("API Request: %s %s", event.Request.Method, event.Request.URL.String())

	ctx := context.WithValue(event.Request.Context(), requestStartTimeKey, time.Now())
	*event.Request = *event.Request.WithContext(ctx)
}

// recordStatusCode thread-safely records HTTP status codes.
func (c *Client) recordStatusCode(statusCode int) {
	c.metrics.statusCodes.Add(strconv.Itoa(statusCode), 1)
}

// onResponse fires once for the final response. Tracks latency and records
// 2xx status codes. Non-2xx status codes are recorded by onError to avoid
// double-counting, since gosip fires both hooks for non-2xx final responses.
func (c *Client) onResponse(event *gosip.HookEvent) {
	if startTime, ok := event.Request.Context().Value(requestStartTimeKey).(time.Time); ok {
		c.metrics.totalDuration.Add(time.Since(startTime).Nanoseconds())
	}

	if event.StatusCode >= 200 && event.StatusCode < 300 {
		c.recordStatusCode(event.StatusCode)
		c.logger.Tracef("API Success: %s %s -> %d (%dms)", event.Request.Method, event.Request.URL.String(), event.StatusCode, c.getRequestDuration(event.Request))
	}
}

// onError fires once per logical request, after all retries are exhausted.
// Increments throttlingCount and networkErrors for the scaler's backpressure
// logic. Status codes are the primary observability mechanism.
func (c *Client) onError(event *gosip.HookEvent) {
	// Record HTTP status code if available
	if event.StatusCode > 0 {
		c.recordStatusCode(event.StatusCode)
	}

	// Throttling counter for scaler backpressure
	if event.StatusCode == 429 || event.StatusCode == 503 {
		c.metrics.throttlingCount.Add(1)
		c.logger.Warnf("API Throttling Error: %s %s -> %d", event.Request.Method, event.Request.URL.String(), event.StatusCode)
		return
	}

	if event.Error == nil {
		c.logger.Errorf("API Error: %s %s -> %d (no error details provided)",
			event.Request.Method, event.Request.URL.String(), event.StatusCode)
		return
	}

	// Network error counter for scaler backpressure
	if isTransientNetError(event.Error) {
		c.metrics.networkErrors.Add(1)
	}

	if event.Request != nil && event.Request.URL != nil {
		c.logger.Errorf("API Error: %s %s -> %v", event.Request.Method, event.Request.URL.String(), event.Error)
	} else {
		c.logger.Errorf("API Error: %v", event.Error)
	}
}

// onRetry fires before each gosip retry attempt. Records status codes for
// intermediate responses and tracks 503 as throttling.
func (c *Client) onRetry(event *gosip.HookEvent) {
	if event.StatusCode != 429 {
		c.recordStatusCode(event.StatusCode)
		if event.StatusCode == 503 {
			c.metrics.throttlingCount.Add(1)
		}
	}
	c.logger.Debugf("API Retry: %s %s -> %d", event.Request.Method, event.Request.URL.String(), event.StatusCode)
}

func (c *Client) getRequestDuration(req *http.Request) int64 {
	if startTime, ok := req.Context().Value(requestStartTimeKey).(time.Time); ok {
		return time.Since(startTime).Milliseconds()
	}
	return 0
}

// GetMetrics returns a snapshot of all API call statistics.
func (c *Client) GetMetrics() model.APIMetrics {
	statusCodes := make(map[int]int64)
	for key, count := range c.metrics.statusCodes.Snapshot() {
		if code, err := strconv.Atoi(key); err == nil {
			statusCodes[code] = count
		}
	}

	totalDuration := c.metrics.totalDuration.Load()
	requestCount := c.metrics.requestCount.Load()
	var avgDuration int64
	if requestCount > 0 {
		avgDuration = (totalDuration / requestCount) / int64(time.Millisecond)
	}

	return model.APIMetrics{
		TotalDuration:    totalDuration,
		AvgDuration:      avgDuration,
		StatusCodes:      statusCodes,
		TransportRetries: c.transportRetries(),
	}
}

// HealthCheck verifies SharePoint connectivity with a minimal API call.
func (c *Client) HealthCheck(ctx context.Context) error {
	sp := api.NewSP(c.gosipClient)
	_, err := sp.Web().Select("Title").Get()
	if err != nil {
		return fmt.Errorf("API health check failed: %w", err)
	}
	return nil
}

// GetSites writes paged site collection records from the tenant admin API to the provided channel.
// The caller owns the channel and is responsible for closing it.
// If onPage is non-nil, it is called after each page is fetched with the number of sites on that page.
func (c *Client) GetSites(ctx context.Context, sites chan<- model.Site, pageSize int, maxPages int, onPage func(int)) error {
	c.logger.Infof("Enumerating SharePoint sites (%d per page, max %d pages)", pageSize, maxPages)

	web := api.NewWeb(c.gosipClient, c.authCfg.SiteURL+"/_api/web", nil)

	list := web.Lists().GetByTitle(TenantAdminSiteCollectionsList).
		Items().
		Select(SiteMetadataFields).
		Top(pageSize)

	page, err := list.GetPaged()
	if err != nil {
		return fmt.Errorf("failed to get first page from SharePoint admin API: %w", err)
	}

	pageNum := 1
	totalSites := 0

	for page != nil && (maxPages == 0 || pageNum <= maxPages) {
		select {
		case <-ctx.Done():
			c.logger.Info("SharePoint enumeration cancelled")
			return ctx.Err()
		default:
		}

		items := page.Items.Data()
		if onPage != nil {
			onPage(len(items))
		}
		c.logger.Debugf("Processing page %d (%d sites)", pageNum, len(items))

		for _, item := range items {
			var site model.Site
			if err := json.Unmarshal(item.Normalized(), &site); err != nil {
				c.logger.Warnf("Failed to unmarshal site data: %v", err)
				continue
			}

			select {
			case sites <- site:
				totalSites++
			case <-ctx.Done():
				c.logger.Info("SharePoint enumeration cancelled")
				return ctx.Err()
			}
		}

		c.logger.Debugf("Page %d complete (%d sites, %d total)", pageNum, len(items), totalSites)

		if !page.HasNextPage() {
			break
		}

		nextPage, err := page.GetNextPage()
		if err != nil {
			return fmt.Errorf("failed to get page %d: %w", pageNum+1, err)
		}
		page = nextPage
		pageNum++
	}

	c.logger.Infof("SharePoint site discovery complete: %d sites found (%d pages)", totalSites, pageNum)
	return nil
}

// newSiteClient creates a gosip.SPClient configured for a specific site URL,
// reusing the parent client's auth, retry, hooks, and transport.
func (c *Client) newSiteClient(siteURL string) *gosip.SPClient {
	auth := &clientcredential.AuthCnfg{
		Base:     entraid.Base{SiteURL: siteURL, TenantID: c.authCfg.TenantID, ClientID: c.authCfg.ClientID},
		CertPath: c.authCfg.CertPath,
	}
	sc := &gosip.SPClient{
		AuthCnfg:      auth,
		RetryPolicies: c.retryPolicy,
		Hooks:         c.gosipClient.Hooks,
	}
	sc.Transport = c.gosipClient.Transport
	return sc
}

// GetSiteUsers retrieves all users with access to a site.
func (c *Client) GetSiteUsers(ctx context.Context, site model.Site) ([]model.SiteUser, error) {
	siteAPI := api.NewSP(c.newSiteClient(site.SiteUrl)).Conf(&api.RequestConfig{Context: ctx})
	users, err := siteAPI.Web().SiteUsers().Get()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch users for site %s: %w", site.SiteUrl, err)
	}

	var siteUsers []*model.SiteUser
	if err := json.Unmarshal(users.Normalized(), &siteUsers); err != nil {
		return nil, fmt.Errorf("failed to unmarshal users for site %s: %w", site.SiteUrl, err)
	}

	result := make([]model.SiteUser, len(siteUsers))
	for i, user := range siteUsers {
		user.SiteId = site.SiteId
		result[i] = *user
	}

	c.logger.Tracef("Retrieved %d users from site %s", len(result), site.SiteUrl)
	return result, nil
}

// GroupFetchResult holds the groups, members, and per-group member fetch
// failure counts returned by [Client.GetSiteGroups].
type GroupFetchResult struct {
	Groups              []model.SiteGroup
	Members             []model.GroupMember
	MemberErrors        int // non-timeout member fetch failures
	MemberTimeoutErrors int // member fetches that hit memberTimeout
}

// GetSiteGroups retrieves all groups and their members for a site.
// Two timeout levels apply: the caller's ctx deadline bounds the entire operation,
// while memberTimeout bounds each individual group's member fetch.
func (c *Client) GetSiteGroups(ctx context.Context, site model.Site, memberTimeout time.Duration) (*GroupFetchResult, error) {
	siteAPI := api.NewSP(c.newSiteClient(site.SiteUrl))
	groups, err := siteAPI.Conf(&api.RequestConfig{Context: ctx}).Web().SiteGroups().Get()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch groups for site %s: %w", site.SiteUrl, err)
	}

	var siteGroups []*model.SiteGroup
	if err := json.Unmarshal(groups.Normalized(), &siteGroups); err != nil {
		return nil, fmt.Errorf("failed to unmarshal groups for site %s: %w", site.SiteUrl, err)
	}

	result := &GroupFetchResult{
		Groups: make([]model.SiteGroup, len(siteGroups)),
	}

	for i, group := range siteGroups {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("parent context cancelled during member fetch for site %s: %w", site.SiteUrl, err)
		}

		group.SiteId = site.SiteId
		result.Groups[i] = *group

		memberCtx, memberCancel := context.WithTimeout(ctx, memberTimeout)
		members, err := siteAPI.Conf(&api.RequestConfig{Context: memberCtx}).Web().SiteGroups().GetByID(group.ID).Users().Get()
		memberCancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				result.MemberTimeoutErrors++
				c.logger.Warnf("Member fetch timed out for group %d on site %s: %v", group.ID, site.SiteUrl, err)
			} else {
				result.MemberErrors++
				c.logger.Debugf("Failed to get members for group %d on site %s: %v", group.ID, site.SiteUrl, err)
			}
			continue
		}

		var groupMembers []*model.GroupMember
		if err := json.Unmarshal(members.Normalized(), &groupMembers); err != nil {
			result.MemberErrors++
			c.logger.Debugf("Failed to unmarshal members for group %d on site %s: %v", group.ID, site.SiteUrl, err)
			continue
		}

		for _, member := range groupMembers {
			member.GroupId = group.ID
			member.SiteId = site.SiteId
			result.Members = append(result.Members, *member)
		}
	}

	c.logger.Debugf("Retrieved %d groups and %d total members from site %s (member errors: %d, member timeouts: %d)",
		len(result.Groups), len(result.Members), site.SiteUrl, result.MemberErrors, result.MemberTimeoutErrors)
	return result, nil
}

// GetUserProfile retrieves detailed profile data from User Profile Service.
func (c *Client) GetUserProfile(ctx context.Context, userID string) (*model.UserProfile, error) {
	sp := api.NewSP(c.gosipClient).Conf(&api.RequestConfig{Context: ctx})

	loginName := strings.ReplaceAll(userID, "'", "''")

	props, err := sp.Profiles().GetPropertiesFor(loginName)
	if err != nil {
		return nil, fmt.Errorf("failed to get profile properties for %s: %w", loginName, err)
	}

	// SID is stored as a claims-encoded string: "i:0h.f|membership|<sid>@live.com"
	// Strip the claims prefix and @live.com suffix to get the raw SID value.
	profileSid := strings.TrimSuffix(strings.TrimPrefix(c.getProfileProperty(props, "SID"), "i:0h.f|membership|"), "@live.com")
	personalSiteState := c.getProfileProperty(props, "SPS-PersonalSiteInstantiationState")
	aadObjectId := c.getProfileProperty(props, "msOnline-ObjectId")

	personalUrl := props.Data().PersonalURL

	return &model.UserProfile{
		AadObjectId:                    aadObjectId,
		AccountName:                    userID,
		PersonalUrl:                    personalUrl,
		ProfileSid:                     profileSid,
		PersonalSiteInstantiationState: personalSiteState,
		// LoginName / UserPrincipalName is claims-encoded: "i:0#.f|membership|user@domain.com" — strip to get the UPN.
		UserPrincipalName: strings.Replace(userID, "i:0#.f|membership|", "", 1),
	}, nil
}

// GetThrottlingCount returns the current throttling count (429 + 503).
func (c *Client) GetThrottlingCount() int64 {
	return c.metrics.throttlingCount.Load()
}

// GetNetworkErrorCount returns the current network error count.
func (c *Client) GetNetworkErrorCount() int64 {
	return c.metrics.networkErrors.Load()
}

// GetTransportStats returns transport-level statistics.
func (c *Client) GetTransportStats() TransportStats {
	return c.transport.TransportStats()
}

func (c *Client) transportRetries() int64 {
	if c.transport != nil {
		return c.transport.TransportStats().TransportRetries
	}
	return 0
}

func (c *Client) getProfileProperty(props api.ProfilePropsResp, key string) string {
	for _, prop := range props.Data().UserProfileProperties {
		if prop.Key == key {
			return prop.Value
		}
	}
	return ""
}

// GetPersonalSites writes OneDrive personal sites to the provided channel using CSOM.
// The caller owns the channel and is responsible for closing it.
// Required because personal sites are not available via REST APIs.
// If onPage is non-nil, it is called after each page is fetched with the number of sites on that page.
func (c *Client) GetPersonalSites(ctx context.Context, sites chan<- model.Site, maxSites int, onPage func(int)) error {
	tenantDomain, err := c.extractTenantDomain(c.authCfg.SiteURL)
	if err != nil {
		return fmt.Errorf("failed to extract tenant domain: %w", err)
	}
	filter := fmt.Sprintf("Url -like '%s%s'", tenantDomain, OneDrivePersonalSitePattern)

	c.logger.Infof("Starting OneDrive site enumeration: filter=%s, maxSites=%d", filter, maxSites)

	rootClient := api.NewHTTPClient(c.gosipClient)
	requestConfig := &api.RequestConfig{Context: ctx}

	startFrom := "0"
	pageNumber := 1
	totalSites := 0
	hasMorePages := true

	for hasMorePages {
		xml, err := csomhelpers.BuildOneDriveQuery(filter, startFrom)
		if err != nil {
			return fmt.Errorf("failed to build CSOM query on page %d: %w", pageNumber, err)
		}

		c.logger.Debugf("Page %d: Executing CSOM query, startFrom: %s", pageNumber, startFrom)

		jsomResp, err := rootClient.ProcessQuery(c.authCfg.SiteURL, bytes.NewBuffer([]byte(xml)), requestConfig)
		if err != nil {
			return fmt.Errorf("CSOM query failed on page %d: %w", pageNumber, err)
		}

		siteData, err := csomhelpers.ParseCSOMResponse(jsomResp)
		if err != nil {
			return fmt.Errorf("failed to parse CSOM response on page %d: %w", pageNumber, err)
		}

		if len(siteData.ChildItems) == 0 {
			c.logger.Debugf("No more sites found at page %d, enumeration complete", pageNumber)
			break
		}

		if onPage != nil {
			onPage(len(siteData.ChildItems))
		}
		c.logger.Debugf("Page %d: Processing %d sites", pageNumber, len(siteData.ChildItems))

		for _, siteItem := range siteData.ChildItems {
			if maxSites > 0 && totalSites >= maxSites {
				c.logger.Infof("Maximum sites limit reached (%d), stopping enumeration", maxSites)
				return nil
			}

			site := c.convertCSOMSiteToModel(siteItem)

			select {
			case sites <- site:
				totalSites++
			case <-ctx.Done():
				c.logger.Debug("OneDrive enumeration cancelled")
				return ctx.Err()
			}
		}

		if len(siteData.NextStartIndexFromSharePoint) > 0 && siteData.NextStartIndexFromSharePoint != startFrom {
			startFrom = siteData.NextStartIndexFromSharePoint
			c.logger.Debugf("Page %d: Updated continuation token", pageNumber)
		} else {
			c.logger.Debugf("Page %d: No new continuation token, enumeration complete", pageNumber)
			hasMorePages = false
		}

		c.logger.Debugf("Page %d: Streamed %d sites (total: %d)", pageNumber, len(siteData.ChildItems), totalSites)
		pageNumber++
	}

	c.logger.Infof("OneDrive enumeration complete: %d total sites (%d pages)", totalSites, pageNumber-1)
	return nil
}

// convertCSOMSiteToModel converts CSOM site data to model.Site, hiding CSOM implementation details
func (c *Client) convertCSOMSiteToModel(siteItem csomhelpers.SiteProperties) model.Site {
	createdTime := csomhelpers.ParseCSOMDate(siteItem.CreatedTime)
	modifiedTime := csomhelpers.ParseCSOMDate(siteItem.LastContentModifiedDate)

	var timeCreatedStr, modifiedStr, lastActivityStr string
	if !createdTime.IsZero() {
		timeCreatedStr = createdTime.Format("2006-01-02T15:04:05Z")
	} else {
		timeCreatedStr = siteItem.CreatedTime
	}

	if !modifiedTime.IsZero() {
		modifiedStr = modifiedTime.Format("2006-01-02T15:04:05Z")
		lastActivityStr = modifiedStr
	} else {
		modifiedStr = siteItem.LastContentModifiedDate
		lastActivityStr = siteItem.LastContentModifiedDate
	}

	return model.Site{
		SiteUrl:        siteItem.Url,
		TimeCreated:    timeCreatedStr,
		Modified:       modifiedStr,
		Title:          siteItem.Title,
		TemplateName:   siteItem.Template,
		CreatedByEmail: siteItem.Owner,
		SiteId:         csomhelpers.CleanSiteId(siteItem.SiteId),
		LastActivityOn: lastActivityStr,
		StorageUsed:    float64(siteItem.StorageUsage),
		LockState:      siteItem.LockState,
	}
}

// extractTenantDomain converts an admin site URL like
// "https://contoso-admin.sharepoint.com" to the OneDrive tenant domain
// "https://contoso-my.sharepoint.com/".
func (c *Client) extractTenantDomain(adminURL string) (string, error) {
	if adminURL == "" {
		return "", fmt.Errorf("admin URL is empty")
	}

	u, err := url.Parse(adminURL)
	if err != nil {
		return "", fmt.Errorf("invalid admin URL %q: %w", adminURL, err)
	}

	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("admin URL %q has no hostname", adminURL)
	}

	tenantPart, _, _ := strings.Cut(host, ".")
	tenantPart = strings.TrimSuffix(tenantPart, "-admin")

	return fmt.Sprintf("https://%s-my.sharepoint.com/", tenantPart), nil
}

// ExecuteCSOM executes a CSOM query against tenant admin endpoints.
// Returns raw response for operations not available via REST APIs.
func (c *Client) ExecuteCSOM(ctx context.Context, query string) ([]byte, error) {
	rootClient := api.NewHTTPClient(c.gosipClient)

	requestConfig := &api.RequestConfig{Context: ctx}
	response, err := rootClient.ProcessQuery(c.authCfg.SiteURL, bytes.NewBuffer([]byte(query)), requestConfig)
	if err != nil {
		return nil, fmt.Errorf("CSOM ProcessQuery failed: %w", err)
	}

	c.logger.Tracef("CSOM query executed successfully, response length: %d bytes", len(response))
	return response, nil
}
