package processors

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"sprawler/internal/config"
	"sprawler/internal/model"
)

func TestSharePointProcessor_Integration(t *testing.T) {
	sites := loadSitesFixture(t, "sp_sites_page1.json")
	siteUsers := loadUsersFixture(t, "sp_siteusers.json")

	mock := &mockAPIClient{
		sites:     sites,
		siteUsers: siteUsers,
	}

	store := newTestStorage(t)

	cfg := &config.Config{
		SharePoint: config.SharePointConfig{
			PageSize:                 100,
			SiteEnumBufferSize:       100,
			SkipTemplates:            []string{"TEAMCHANNEL#0", "TEAMCHANNEL#1", "APPCATALOG#0", "REDIRECTSITE#0"},
			UserWorkers:              2,
			GroupWorkers:             1,
			ProcessGroups:            false,
			ProcessGroupMembers:      false,
			UserFetchTimeout:         30 * time.Second,
			GroupFetchTimeout:        30 * time.Second,
			MemberFetchTimeout:       10 * time.Second,
			ProgressReportInterval:   1 * time.Hour,
			ThrottleRecoveryCooldown: 1 * time.Minute,
		},
	}

	processor := NewSharePointProcessor(cfg, store, mock)
	result := processor.Process(context.Background())

	// 8 total sites, 2 filtered (TEAMCHANNEL + APPCATALOG) = 6 processable
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.ErrorMsg)
	}
	if result.RecordsExtracted != 6 {
		t.Fatalf("RecordsExtracted = %d, want 6", result.RecordsExtracted)
	}
	if result.RecordsFailed != 0 {
		t.Fatalf("RecordsFailed = %d, want 0", result.RecordsFailed)
	}

	store.Close()
	db := openTestDB(t, store.dbPath)

	// --- Filtered sites NOT in DB ---
	assertQueryInt(t, db, 0, `SELECT COUNT(*) FROM sites WHERE template_name IN ('TEAMCHANNEL#1', 'APPCATALOG#0')`)

	// --- Verify the 6 specific site_ids ---
	expectedSiteIDs := []string{
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		"33333333-3333-3333-3333-333333333333",
		"44444444-4444-4444-4444-444444444444",
		"55555555-5555-5555-5555-555555555555",
		"88888888-8888-8888-8888-888888888888",
	}
	for _, id := range expectedSiteIDs {
		assertQueryInt(t, db, 1, `SELECT COUNT(*) FROM sites WHERE site_id = ?`, id)
	}

	// --- Per-site user counts ---
	perSiteUsers := map[string]int64{
		"11111111-1111-1111-1111-111111111111": 4, // engineering
		"22222222-2222-2222-2222-222222222222": 2, // marketing
		"33333333-3333-3333-3333-333333333333": 3, // sales
		"44444444-4444-4444-4444-444444444444": 1, // hr
		"55555555-5555-5555-5555-555555555555": 3, // finance
		"88888888-8888-8888-8888-888888888888": 2, // legal
	}
	for siteID, wantCount := range perSiteUsers {
		assertQueryInt(t, db, wantCount, `SELECT COUNT(*) FROM site_users WHERE site_id = ?`, siteID)
	}

	// --- Referential integrity: every user has a valid site_id ---
	assertQueryInt(t, db, 0, `SELECT COUNT(*) FROM site_users WHERE site_id NOT IN (SELECT site_id FROM sites)`)

	// --- No users with empty site_id ---
	assertQueryInt(t, db, 0, `SELECT COUNT(*) FROM site_users WHERE site_id = ''`)

	// --- Spot-check alice in engineering ---
	var loginName, email string
	var isSiteAdmin bool
	var principalType int
	err := db.QueryRow(`SELECT login_name, email, is_site_admin, principal_type FROM site_users WHERE id = 10 AND site_id = '11111111-1111-1111-1111-111111111111'`).
		Scan(&loginName, &email, &isSiteAdmin, &principalType)
	if err != nil {
		t.Fatalf("spot-check alice: %v", err)
	}
	if loginName != "i:0#.f|membership|alice@contoso.com" {
		t.Fatalf("alice loginName = %q, want claims format", loginName)
	}
	if email != "alice@contoso.com" {
		t.Fatalf("alice email = %q, want alice@contoso.com", email)
	}
	if !isSiteAdmin {
		t.Fatal("alice should be site admin")
	}
	if principalType != 1 {
		t.Fatalf("alice principalType = %d, want 1", principalType)
	}

	// --- System accounts preserved ---
	assertQueryInt(t, db, 1, `SELECT COUNT(*) FROM site_users WHERE login_name = 'SHAREPOINT\system'`)

	// --- Security groups (PrincipalType=4) ---
	assertQueryInt(t, db, 2, `SELECT COUNT(*) FROM site_users WHERE principal_type = 4`)

	// --- Null UPN handled for system account ---
	var upn sql.NullString
	err = db.QueryRow(`SELECT user_principal_name FROM site_users WHERE login_name = 'SHAREPOINT\system'`).Scan(&upn)
	if err != nil {
		t.Fatalf("query system UPN: %v", err)
	}
	if upn.Valid && upn.String != "" {
		t.Fatalf("system account UPN should be null/empty, got %q", upn.String)
	}
}

func TestSharePointProcessor_Integration_WithGroups(t *testing.T) {
	sites := loadSitesFixture(t, "sp_sites_page1.json")
	siteUsers := loadUsersFixture(t, "sp_siteusers.json")
	groupResults := buildGroupResults(t, "sp_sitegroups.json", "sp_groupmembers.json")

	mock := &mockAPIClient{
		sites:        sites,
		siteUsers:    siteUsers,
		groupResults: groupResults,
	}

	store := newTestStorage(t)

	cfg := &config.Config{
		SharePoint: config.SharePointConfig{
			PageSize:                 100,
			SiteEnumBufferSize:       100,
			SkipTemplates:            []string{"TEAMCHANNEL#0", "TEAMCHANNEL#1", "APPCATALOG#0", "REDIRECTSITE#0"},
			UserWorkers:              2,
			GroupWorkers:             2,
			ProcessGroups:            true,
			ProcessGroupMembers:      true,
			UserFetchTimeout:         30 * time.Second,
			GroupFetchTimeout:        30 * time.Second,
			MemberFetchTimeout:       10 * time.Second,
			ProgressReportInterval:   1 * time.Hour,
			ThrottleRecoveryCooldown: 1 * time.Minute,
		},
	}

	processor := NewSharePointProcessor(cfg, store, mock)
	result := processor.Process(context.Background())

	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.ErrorMsg)
	}
	if result.RecordsExtracted != 6 {
		t.Fatalf("RecordsExtracted = %d, want 6", result.RecordsExtracted)
	}

	store.Close()
	db := openTestDB(t, store.dbPath)

	// 6 groups total: eng=2, marketing=1, sales=1, hr=0, finance=1, legal=1
	groupCount := queryCount(t, db, "site_groups")
	if groupCount != 6 {
		t.Fatalf("site_groups count = %d, want 6", groupCount)
	}

	// Group-to-site referential integrity
	assertQueryInt(t, db, 0, `SELECT COUNT(*) FROM site_groups WHERE site_id NOT IN (SELECT site_id FROM sites)`)

	// Per-site group counts
	assertQueryInt(t, db, 2, `SELECT COUNT(*) FROM site_groups WHERE site_id = '11111111-1111-1111-1111-111111111111'`) // engineering
	assertQueryInt(t, db, 1, `SELECT COUNT(*) FROM site_groups WHERE site_id = '22222222-2222-2222-2222-222222222222'`) // marketing
	assertQueryInt(t, db, 0, `SELECT COUNT(*) FROM site_groups WHERE site_id = '44444444-4444-4444-4444-444444444444'`) // hr (empty)

	// Members: eng(1+2)=3, marketing=1, sales=2, finance=1, legal=1 = 8
	memberCount := queryCount(t, db, "group_members")
	if memberCount != 8 {
		t.Fatalf("group_members count = %d, want 8", memberCount)
	}

	// Member-to-group referential integrity
	assertQueryInt(t, db, 0, `SELECT COUNT(*) FROM group_members WHERE (group_id, site_id) NOT IN (SELECT id, site_id FROM site_groups)`)

	// Spot-check: alice is member of engineering group 3
	var memberLogin string
	var memberPrincipalType int
	err := db.QueryRow(`SELECT login_name, principal_type FROM group_members WHERE id = 10 AND group_id = 3 AND site_id = '11111111-1111-1111-1111-111111111111'`).
		Scan(&memberLogin, &memberPrincipalType)
	if err != nil {
		t.Fatalf("spot-check group member: %v", err)
	}
	if memberLogin != "i:0#.f|membership|alice@contoso.com" {
		t.Fatalf("member login = %q, want alice claims", memberLogin)
	}
	if memberPrincipalType != 1 {
		t.Fatalf("member principalType = %d, want 1", memberPrincipalType)
	}
}

func TestSharePointProcessor_Integration_Failure(t *testing.T) {
	sites := loadSitesFixture(t, "sp_sites_page1.json")
	siteUsers := loadUsersFixture(t, "sp_siteusers.json")

	mock := &mockAPIClient{
		sites:     sites,
		siteUsers: siteUsers,
		errorSiteUsers: map[string]error{
			"https://contoso.sharepoint.com/sites/hr": fmt.Errorf("500 Internal Server Error"),
		},
	}

	store := newTestStorage(t)

	cfg := &config.Config{
		SharePoint: config.SharePointConfig{
			PageSize:                 100,
			SiteEnumBufferSize:       100,
			SkipTemplates:            []string{"TEAMCHANNEL#0", "TEAMCHANNEL#1", "APPCATALOG#0", "REDIRECTSITE#0"},
			UserWorkers:              2,
			GroupWorkers:             1,
			ProcessGroups:            false,
			ProcessGroupMembers:      false,
			UserFetchTimeout:         30 * time.Second,
			GroupFetchTimeout:        30 * time.Second,
			MemberFetchTimeout:       10 * time.Second,
			ProgressReportInterval:   1 * time.Hour,
			ThrottleRecoveryCooldown: 1 * time.Minute,
		},
	}

	processor := NewSharePointProcessor(cfg, store, mock)
	result := processor.Process(context.Background())

	// 5 succeed, 1 fails (hr returns 500)
	if result.RecordsFailed != 1 {
		t.Fatalf("RecordsFailed = %d, want 1", result.RecordsFailed)
	}
	if result.RecordsExtracted != 5 {
		t.Fatalf("RecordsExtracted = %d, want 5", result.RecordsExtracted)
	}

	// Outcome recorded
	if len(result.SPOutcomes) != 1 {
		t.Fatalf("SPOutcomes = %d, want 1", len(result.SPOutcomes))
	}
	if result.SPOutcomes[0].UsersFailure == nil {
		t.Fatal("UsersFailure should be set for failed site")
	}
	if result.SPOutcomes[0].UsersFailure.Category != "server_error" {
		t.Fatalf("UsersFailure.Category = %q, want server_error", result.SPOutcomes[0].UsersFailure.Category)
	}

	// Verify site_outcomes table
	store.Close()
	db := openTestDB(t, store.dbPath)
	assertQueryInt(t, db, 1, `SELECT COUNT(*) FROM site_outcomes WHERE processor = 'sharepoint'`)
}

// --- finalizeSiteOutcome unit tests ---

func TestFinalizeSiteOutcome(t *testing.T) {
	t.Run("all success", func(t *testing.T) {
		var outcomes sync.Map
		counters := &sharePointCounters{}
		oc := &OutcomeCollector{}

		tracker := &spSiteTracker{
			site:      model.Site{SiteUrl: "https://example.com/sites/a", SiteId: "aaa"},
			startedAt: time.Now(),
		}
		tracker.remaining.Store(1)
		outcomes.Store(tracker.site.SiteUrl, tracker)

		finalizeSiteOutcome(&outcomes, tracker, counters, oc)

		if counters.Succeeded.Load() != 1 {
			t.Fatalf("Succeeded = %d, want 1", counters.Succeeded.Load())
		}
		if counters.Failed.Load() != 0 {
			t.Fatalf("Failed = %d, want 0", counters.Failed.Load())
		}
		if len(oc.SPOutcomes()) != 0 {
			t.Fatalf("SPOutcomes = %d, want 0", len(oc.SPOutcomes()))
		}
		// Tracker should be deleted from outcomes
		if _, ok := outcomes.Load(tracker.site.SiteUrl); ok {
			t.Fatal("tracker not deleted from outcomes map")
		}
	})

	t.Run("users failure records outcome", func(t *testing.T) {
		var outcomes sync.Map
		counters := &sharePointCounters{}
		oc := &OutcomeCollector{}

		tracker := &spSiteTracker{
			site:      model.Site{SiteUrl: "https://example.com/sites/b", SiteId: "bbb"},
			startedAt: time.Now(),
		}
		tracker.remaining.Store(1)
		tracker.usersFailure.Store(&model.OperationFailure{
			Category:   model.ErrServerError,
			HTTPStatus: 500,
			Detail:     "500 Internal Server Error",
		})
		outcomes.Store(tracker.site.SiteUrl, tracker)

		finalizeSiteOutcome(&outcomes, tracker, counters, oc)

		if counters.Failed.Load() != 1 {
			t.Fatalf("Failed = %d, want 1", counters.Failed.Load())
		}
		if counters.Succeeded.Load() != 0 {
			t.Fatalf("Succeeded = %d, want 0", counters.Succeeded.Load())
		}
		results := oc.SPOutcomes()
		if len(results) != 1 {
			t.Fatalf("SPOutcomes = %d, want 1", len(results))
		}
		if results[0].UsersFailure == nil {
			t.Fatal("UsersFailure should be set")
		}
		if results[0].UsersFailure.Category != model.ErrServerError {
			t.Fatalf("UsersFailure.Category = %s, want %s", results[0].UsersFailure.Category, model.ErrServerError)
		}
	})

	t.Run("member timeout errors count as failure", func(t *testing.T) {
		var outcomes sync.Map
		counters := &sharePointCounters{}
		oc := &OutcomeCollector{}

		tracker := &spSiteTracker{
			site:      model.Site{SiteUrl: "https://example.com/sites/c", SiteId: "ccc"},
			startedAt: time.Now(),
		}
		tracker.remaining.Store(1)
		tracker.memberTimeoutErrors.Store(2)
		outcomes.Store(tracker.site.SiteUrl, tracker)

		finalizeSiteOutcome(&outcomes, tracker, counters, oc)

		if counters.Failed.Load() != 1 {
			t.Fatalf("Failed = %d, want 1", counters.Failed.Load())
		}
		results := oc.SPOutcomes()
		if len(results) != 1 {
			t.Fatalf("SPOutcomes = %d, want 1", len(results))
		}
		if results[0].MemberTimeoutErrors != 2 {
			t.Fatalf("MemberTimeoutErrors = %d, want 2", results[0].MemberTimeoutErrors)
		}
	})

	t.Run("remaining=2 first call does not finalize", func(t *testing.T) {
		var outcomes sync.Map
		counters := &sharePointCounters{}
		oc := &OutcomeCollector{}

		tracker := &spSiteTracker{
			site:      model.Site{SiteUrl: "https://example.com/sites/d", SiteId: "ddd"},
			startedAt: time.Now(),
		}
		tracker.remaining.Store(2)
		outcomes.Store(tracker.site.SiteUrl, tracker)

		// First call: remaining 2 -> 1, should not finalize
		finalizeSiteOutcome(&outcomes, tracker, counters, oc)

		if counters.SitesCompleted.Load() != 0 {
			t.Fatalf("SitesCompleted = %d after first call, want 0", counters.SitesCompleted.Load())
		}
		// Tracker should still be in map
		if _, ok := outcomes.Load(tracker.site.SiteUrl); !ok {
			t.Fatal("tracker should still be in outcomes after first call")
		}

		// Second call: remaining 1 -> 0, should finalize
		finalizeSiteOutcome(&outcomes, tracker, counters, oc)

		if counters.SitesCompleted.Load() != 1 {
			t.Fatalf("SitesCompleted = %d after second call, want 1", counters.SitesCompleted.Load())
		}
		if counters.Succeeded.Load() != 1 {
			t.Fatalf("Succeeded = %d, want 1", counters.Succeeded.Load())
		}
		if _, ok := outcomes.Load(tracker.site.SiteUrl); ok {
			t.Fatal("tracker should be deleted after second call")
		}
	})

	t.Run("groups failure only", func(t *testing.T) {
		var outcomes sync.Map
		counters := &sharePointCounters{}
		oc := &OutcomeCollector{}

		tracker := &spSiteTracker{
			site:      model.Site{SiteUrl: "https://example.com/sites/e", SiteId: "eee"},
			startedAt: time.Now(),
		}
		tracker.remaining.Store(1)
		tracker.groupsFailure.Store(&model.OperationFailure{
			Category:   model.ErrTimeout,
			HTTPStatus: 0,
			Detail:     "context deadline exceeded",
		})
		outcomes.Store(tracker.site.SiteUrl, tracker)

		finalizeSiteOutcome(&outcomes, tracker, counters, oc)

		if counters.Failed.Load() != 1 {
			t.Fatalf("Failed = %d, want 1", counters.Failed.Load())
		}
		results := oc.SPOutcomes()
		if len(results) != 1 {
			t.Fatalf("SPOutcomes = %d, want 1", len(results))
		}
		if results[0].GroupsFailure == nil {
			t.Fatal("GroupsFailure should be set")
		}
	})

	t.Run("member errors only", func(t *testing.T) {
		var outcomes sync.Map
		counters := &sharePointCounters{}
		oc := &OutcomeCollector{}

		tracker := &spSiteTracker{
			site:      model.Site{SiteUrl: "https://example.com/sites/f", SiteId: "fff"},
			startedAt: time.Now(),
		}
		tracker.remaining.Store(1)
		tracker.memberErrors.Store(3)
		outcomes.Store(tracker.site.SiteUrl, tracker)

		finalizeSiteOutcome(&outcomes, tracker, counters, oc)

		if counters.Failed.Load() != 1 {
			t.Fatalf("Failed = %d, want 1", counters.Failed.Load())
		}
		results := oc.SPOutcomes()
		if len(results) != 1 {
			t.Fatalf("SPOutcomes = %d, want 1", len(results))
		}
		if results[0].MemberErrors != 3 {
			t.Fatalf("MemberErrors = %d, want 3", results[0].MemberErrors)
		}
	})
}
