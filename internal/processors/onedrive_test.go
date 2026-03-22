package processors

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"sprawler/internal/config"
	"sprawler/internal/model"
)

// --- Unit tests ---

func TestBuildProfileDataFromSite(t *testing.T) {
	p := &OneDriveProcessor{}

	t.Run("email produces claims account name", func(t *testing.T) {
		site := model.Site{
			SiteUrl:        "https://contoso-my.sharepoint.com/personal/user_contoso_com",
			CreatedByEmail: "user@contoso.com",
			LastActivityOn: "2024-06-01T08:30:00Z",
		}
		result := p.buildProfileDataFromSite(site)

		if result.AccountName != "i:0#.f|membership|user@contoso.com" {
			t.Fatalf("AccountName = %q, want claims format", result.AccountName)
		}
		if result.PersonalUrl != site.SiteUrl {
			t.Fatalf("PersonalUrl = %q, want %q", result.PersonalUrl, site.SiteUrl)
		}
		if result.LastModifiedTime != site.LastActivityOn {
			t.Fatalf("LastModifiedTime = %q, want %q", result.LastModifiedTime, site.LastActivityOn)
		}
	})

	t.Run("empty email produces empty account name", func(t *testing.T) {
		site := model.Site{
			SiteUrl:        "https://contoso-my.sharepoint.com/personal/orphan",
			CreatedByEmail: "",
		}
		result := p.buildProfileDataFromSite(site)

		if result.AccountName != "" {
			t.Fatalf("AccountName = %q, want empty", result.AccountName)
		}
	})
}

func TestNormalizePersonalUrl(t *testing.T) {
	p := &OneDriveProcessor{}

	tests := []struct {
		in   string
		want string
	}{
		{"https://contoso.com/personal/user/", "https://contoso.com/personal/user"},
		{"https://contoso.com/personal/user", "https://contoso.com/personal/user"},
		{"", ""},
	}
	for _, tt := range tests {
		got := p.normalizePersonalUrl(tt.in)
		if got != tt.want {
			t.Errorf("normalizePersonalUrl(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// --- Integration tests ---

func TestOneDriveProcessor_Integration(t *testing.T) {
	personalSites := loadPersonalSitesFixture(t, "od_sites.json")
	siteUsers := loadUsersFixture(t, "od_siteusers.json")
	profiles := loadProfilesFixture(t, "od_profiles.json")

	mock := &mockAPIClient{
		personalSites: personalSites,
		siteUsers:     siteUsers,
		profiles:      profiles,
	}

	store := newTestStorage(t)

	cfg := &config.Config{
		OneDrive: config.OneDriveConfig{
			CSOMBufferSize:           100,
			UserWorkers:              2,
			UserFetchTimeout:         30 * time.Second,
			ProfileFetchTimeout:      30 * time.Second,
			ProgressReportInterval:   1 * time.Hour,
			ThrottleRecoveryCooldown: 1 * time.Minute,
		},
	}

	processor := NewOneDriveProcessor(cfg, store, mock)
	result := processor.Process(context.Background())

	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.ErrorMsg)
	}
	if result.RecordsExtracted != 5 {
		t.Fatalf("RecordsExtracted = %d, want 5", result.RecordsExtracted)
	}
	if result.RecordsFailed != 0 {
		t.Fatalf("RecordsFailed = %d, want 0", result.RecordsFailed)
	}

	store.Close()
	db := openTestDB(t, store.dbPath)

	// --- All 5 sites written (including locked + orphan) ---
	siteCount := queryCount(t, db, "sites")
	if siteCount != 5 {
		t.Fatalf("sites count = %d, want 5", siteCount)
	}

	// --- Users: user1=2, user2=1, user3=1, orphan=1 = 5 (locked site skipped) ---
	userCount := queryCount(t, db, "site_users")
	if userCount != 5 {
		t.Fatalf("site_users count = %d, want 5", userCount)
	}

	// --- Profiles: user1 + user2 + user3 + locked_user = 4 ---
	profileCount := queryCount(t, db, "user_profiles")
	if profileCount != 4 {
		t.Fatalf("user_profiles count = %d, want 4", profileCount)
	}

	// --- Locked site is in DB ---
	assertQueryInt(t, db, 1, `SELECT COUNT(*) FROM sites WHERE site_url LIKE '%locked_user%'`)

	// --- Locked site has NO users (users skipped due to lock state) ---
	assertQueryInt(t, db, 0, `SELECT COUNT(*) FROM site_users WHERE site_id = 'dddddddd-dddd-dddd-dddd-dddddddddddd'`)

	// --- Locked site still has a profile ---
	assertQueryInt(t, db, 1, `SELECT COUNT(*) FROM user_profiles WHERE personal_url LIKE '%locked_user%'`)

	// --- Orphan site is in DB ---
	assertQueryInt(t, db, 1, `SELECT COUNT(*) FROM sites WHERE site_url LIKE '%orphan%'`)

	// --- Orphan site has users (system account) ---
	assertQueryInt(t, db, 1, `SELECT COUNT(*) FROM site_users WHERE site_id = 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee'`)

	// --- Orphan site has NO profile ---
	assertQueryInt(t, db, 0, `SELECT COUNT(*) FROM user_profiles WHERE personal_url LIKE '%orphan%'`)

	// --- Profile data correctness: spot-check user1 ---
	var aadObjID, profileSid, accountName string
	err := db.QueryRow(`SELECT aad_object_id, profile_sid, account_name FROM user_profiles WHERE personal_url LIKE '%user1%'`).
		Scan(&aadObjID, &profileSid, &accountName)
	if err != nil {
		t.Fatalf("spot-check user1 profile: %v", err)
	}
	if aadObjID != "aaa11111-1111-1111-1111-111111111111" {
		t.Fatalf("user1 aad_object_id = %q, want aaa11111-...", aadObjID)
	}
	if profileSid != "S-1-5-21-1111111111-1111111111-1111111111-11111" {
		t.Fatalf("user1 profile_sid = %q, want S-1-5-21-...", profileSid)
	}
	if accountName != "i:0#.f|membership|user1@contoso.com" {
		t.Fatalf("user1 account_name = %q, want claims format", accountName)
	}

	// --- PersonalUrl normalization: no trailing slashes ---
	var trailingSlashCount int64
	err = db.QueryRow(`SELECT COUNT(*) FROM user_profiles WHERE personal_url LIKE '%/'`).Scan(&trailingSlashCount)
	if err != nil {
		t.Fatalf("query trailing slashes: %v", err)
	}
	if trailingSlashCount != 0 {
		t.Fatalf("found %d profiles with trailing slash, want 0", trailingSlashCount)
	}

	// --- UPN extracted from claims ---
	var upn sql.NullString
	err = db.QueryRow(`SELECT user_principal_name FROM user_profiles WHERE personal_url LIKE '%user1%'`).Scan(&upn)
	if err != nil {
		t.Fatalf("query user1 UPN: %v", err)
	}
	if !upn.Valid || upn.String != "user1@contoso.com" {
		t.Fatalf("user1 UPN = %q, want user1@contoso.com", upn.String)
	}
}

func TestOneDriveProcessor_Integration_ProfileFailure(t *testing.T) {
	personalSites := loadPersonalSitesFixture(t, "od_sites.json")
	siteUsers := loadUsersFixture(t, "od_siteusers.json")
	profiles := loadProfilesFixture(t, "od_profiles.json")

	mock := &mockAPIClient{
		personalSites: personalSites,
		siteUsers:     siteUsers,
		profiles:      profiles,
		errorProfiles: map[string]error{
			"i:0#.f|membership|user2@contoso.com": fmt.Errorf("500 Internal Server Error"),
		},
	}

	store := newTestStorage(t)

	cfg := &config.Config{
		OneDrive: config.OneDriveConfig{
			CSOMBufferSize:           100,
			UserWorkers:              2,
			UserFetchTimeout:         30 * time.Second,
			ProfileFetchTimeout:      30 * time.Second,
			ProgressReportInterval:   1 * time.Hour,
			ThrottleRecoveryCooldown: 1 * time.Minute,
		},
	}

	processor := NewOneDriveProcessor(cfg, store, mock)
	result := processor.Process(context.Background())

	if result.RecordsFailed != 1 {
		t.Fatalf("RecordsFailed = %d, want 1", result.RecordsFailed)
	}
	store.Close()

	// Find the user2 outcome
	var found bool
	for _, o := range result.ODOutcomes {
		if o.UserAccount == "i:0#.f|membership|user2@contoso.com" {
			found = true
			if o.ProfileFailure == nil {
				t.Fatal("ProfileFailure should be set for user2")
			}
			break
		}
	}
	if !found {
		t.Fatalf("no outcome found for user2, got %d outcomes", len(result.ODOutcomes))
	}
}
