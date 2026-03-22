package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sprawler/internal/model"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestStorage(t *testing.T, opts ...func(*Config)) Storage {
	t.Helper()
	cfg := Config{
		Path:          t.TempDir(),
		Name:          "test.db",
		Recreate:      true,
		BatchSize:     100,
		FlushInterval: 0, // flush only on Close, keeping tests deterministic
		MaxConns:      1,
	}
	for _, o := range opts {
		o(&cfg)
	}
	s, err := NewSQLiteStorage(cfg)
	if err != nil {
		t.Fatalf("NewSQLiteStorage: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func rawDB(t *testing.T, s Storage) *sql.DB {
	t.Helper()
	ss, ok := s.(*sqliteStorage)
	if !ok {
		t.Fatal("storage is not *sqliteStorage")
	}
	return ss.db
}

// openDB opens a fresh read-only connection to the storage's DB file.
// Use this for verification after Close() has been called.
func openDB(t *testing.T, s Storage) *sql.DB {
	t.Helper()
	ss, ok := s.(*sqliteStorage)
	if !ok {
		t.Fatal("storage is not *sqliteStorage")
	}
	db, err := sql.Open("sqlite3", ss.dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func rowCount(t *testing.T, db *sql.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("rowCount(%s): %v", table, err)
	}
	return n
}

// ---------------------------------------------------------------------------
// test data factories
// ---------------------------------------------------------------------------

func testSite(id string) model.Site {
	return model.Site{
		SiteId:  id,
		SiteUrl: "https://contoso.sharepoint.com/sites/" + id,
		Title:   "Site " + id,
	}
}

func testUser(id int, siteID string) model.SiteUser {
	return model.SiteUser{
		ID:        id,
		SiteId:    siteID,
		LoginName: fmt.Sprintf("i:0#.f|membership|user%d@contoso.com", id),
		Title:     fmt.Sprintf("User %d", id),
	}
}

func testProfile(url string) model.UserProfile {
	return model.UserProfile{
		PersonalUrl:       url,
		UserPrincipalName: "user@contoso.com",
	}
}

func testGroup(id int, siteID string) model.SiteGroup {
	return model.SiteGroup{
		ID:        id,
		SiteId:    siteID,
		Title:     fmt.Sprintf("Group %d", id),
		LoginName: fmt.Sprintf("Group %d", id),
		Updated:   "2024-01-01",
	}
}

func testMember(id, groupID int, siteID string) model.GroupMember {
	return model.GroupMember{
		ID:            id,
		GroupId:       groupID,
		SiteId:        siteID,
		Title:         fmt.Sprintf("Member %d", id),
		LoginName:     fmt.Sprintf("i:0#.f|membership|member%d@contoso.com", id),
		PrincipalType: 1,
		Updated:       "2024-01-01",
	}
}

// ---------------------------------------------------------------------------
// Constructor & Lifecycle
// ---------------------------------------------------------------------------

func TestNewSQLiteStorage_CreatesDB(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLiteStorage(Config{
		Path:      dir,
		Name:      "test.db",
		Recreate:  true,
		BatchSize: 100,
		MaxConns:  1,
	})
	if err != nil {
		t.Fatalf("NewSQLiteStorage: %v", err)
	}
	defer s.Close()

	if s == nil {
		t.Fatal("expected non-nil storage")
	}
	if _, err := os.Stat(filepath.Join(dir, "test.db")); err != nil {
		t.Fatalf("DB file should exist: %v", err)
	}
	if err := s.HealthCheck(); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
}

func TestNewSQLiteStorage_RecreateRemovesExisting(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Path:      dir,
		Name:      "test.db",
		Recreate:  true,
		BatchSize: 100,
		MaxConns:  1,
	}

	// First storage: insert a site.
	s1, err := NewSQLiteStorage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan model.Site, 1)
	ch <- testSite("s1")
	close(ch)
	s1.StreamSites(context.Background(), ch)
	s1.Close()

	// Second storage with Recreate: true on same path.
	s2, err := NewSQLiteStorage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	if got := rowCount(t, rawDB(t, s2), "sites"); got != 0 {
		t.Fatalf("expected 0 sites after recreate, got %d", got)
	}
}

func TestClose_IsIdempotent(t *testing.T) {
	s := newTestStorage(t)
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Streaming -- Sites (full pipeline)
// ---------------------------------------------------------------------------

func TestStreamSites_PersistsToDatabase(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	ch := make(chan model.Site, 3)
	ch <- testSite("a")
	ch <- testSite("b")
	ch <- testSite("c")
	close(ch)

	s.StreamSites(ctx, ch)

	// Close flushes pending batches; openDB reopens read-only to verify data hit disk.
	s.Close()

	db := openDB(t, s)
	if got := rowCount(t, db, "sites"); got != 3 {
		t.Fatalf("expected 3 sites, got %d", got)
	}
}

func TestStreamSites_ContextCancellation(t *testing.T) {
	s := newTestStorage(t)
	ctx, cancel := context.WithCancel(context.Background())

	// Unbuffered channel + goroutine: sender blocks on each send, so
	// cancellation can interrupt mid-stream rather than after all 1000 buffer.
	ch := make(chan model.Site)
	go func() {
		defer close(ch)
		for i := range 1000 {
			select {
			case ch <- testSite(fmt.Sprintf("s%d", i)):
			case <-ctx.Done():
				return
			}
		}
	}()

	// Let some items flow, then cancel.
	time.Sleep(1 * time.Millisecond)
	cancel()
	s.StreamSites(ctx, ch)
	s.Close()

	got := rowCount(t, openDB(t, s), "sites")
	if got >= 1000 {
		t.Fatalf("expected cancellation to stop early, got all %d sites", got)
	}
}

// ---------------------------------------------------------------------------
// Streaming -- Other Entities
// ---------------------------------------------------------------------------

func TestStreamUsers_PersistsToDatabase(t *testing.T) {
	s := newTestStorage(t)
	ch := make(chan model.SiteUser, 3)
	ch <- testUser(1, "site-a")
	ch <- testUser(2, "site-a")
	ch <- testUser(3, "site-b")
	close(ch)

	s.StreamUsers(context.Background(), ch)
	s.Close()

	if got := rowCount(t, openDB(t, s), "site_users"); got != 3 {
		t.Fatalf("expected 3, got %d", got)
	}
}

func TestStreamProfiles_PersistsToDatabase(t *testing.T) {
	s := newTestStorage(t)
	ch := make(chan model.UserProfile, 2)
	ch <- testProfile("https://contoso-my.sharepoint.com/personal/user1")
	ch <- testProfile("https://contoso-my.sharepoint.com/personal/user2")
	close(ch)

	s.StreamProfiles(context.Background(), ch)
	s.Close()

	if got := rowCount(t, openDB(t, s), "user_profiles"); got != 2 {
		t.Fatalf("expected 2, got %d", got)
	}
}

func TestStreamGroups_PersistsToDatabase(t *testing.T) {
	s := newTestStorage(t)
	ch := make(chan model.SiteGroup, 2)
	ch <- testGroup(1, "site-a")
	ch <- testGroup(2, "site-a")
	close(ch)

	s.StreamGroups(context.Background(), ch)
	s.Close()

	if got := rowCount(t, openDB(t, s), "site_groups"); got != 2 {
		t.Fatalf("expected 2, got %d", got)
	}
}

func TestStreamMembers_PersistsToDatabase(t *testing.T) {
	s := newTestStorage(t)
	ch := make(chan model.GroupMember, 2)
	ch <- testMember(1, 10, "site-a")
	ch <- testMember(2, 10, "site-a")
	close(ch)

	s.StreamMembers(context.Background(), ch)
	s.Close()

	if got := rowCount(t, openDB(t, s), "group_members"); got != 2 {
		t.Fatalf("expected 2, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Transaction Atomicity
// ---------------------------------------------------------------------------

func TestStreamSites_BatchRollsBackOnError(t *testing.T) {
	s := newTestStorage(t) // Recreate: true -> INSERT mode (no upsert)

	// Batch of 3 sites where the 3rd duplicates the 1st primary key.
	// INSERT will fail on the duplicate, rolling back the entire transaction.
	ch := make(chan model.Site, 3)
	ch <- testSite("dup")
	ch <- testSite("other")
	ch <- testSite("dup") // PK violation
	close(ch)

	s.StreamSites(context.Background(), ch)
	s.Close()

	db := openDB(t, s)
	if got := rowCount(t, db, "sites"); got != 0 {
		t.Fatalf("expected 0 sites after rollback, got %d", got)
	}
}

func TestStreamMixedKinds_SingleTransaction(t *testing.T) {
	s := newTestStorage(t)

	// Interleave sites and users in the same flush.
	siteCh := make(chan model.Site, 2)
	userCh := make(chan model.SiteUser, 2)
	siteCh <- testSite("s1")
	siteCh <- testSite("s2")
	userCh <- testUser(1, "s1")
	userCh <- testUser(2, "s2")
	close(siteCh)
	close(userCh)

	s.StreamSites(context.Background(), siteCh)
	s.StreamUsers(context.Background(), userCh)
	s.Close()

	db := openDB(t, s)
	if got := rowCount(t, db, "sites"); got != 2 {
		t.Fatalf("expected 2 sites, got %d", got)
	}
	if got := rowCount(t, db, "site_users"); got != 2 {
		t.Fatalf("expected 2 users, got %d", got)
	}
}

func TestStreamMixedKinds_RollbackAcrossKinds(t *testing.T) {
	s := newTestStorage(t) // Recreate: true -> INSERT mode
	ss := s.(*sqliteStorage)

	// First flush: insert site + user (commits successfully).
	siteCh := make(chan model.Site, 1)
	siteCh <- testSite("s1")
	close(siteCh)
	s.StreamSites(context.Background(), siteCh)

	userCh := make(chan model.SiteUser, 1)
	userCh <- testUser(1, "s1")
	close(userCh)
	s.StreamUsers(context.Background(), userCh)
	ss.queue.Flush()

	// Second flush: new user + duplicate site in the same transaction.
	// The PK violation on the site rolls back the user too.
	userCh2 := make(chan model.SiteUser, 1)
	userCh2 <- testUser(2, "s1")
	close(userCh2)
	s.StreamUsers(context.Background(), userCh2)

	siteCh2 := make(chan model.Site, 1)
	siteCh2 <- testSite("s1") // PK violation
	close(siteCh2)
	s.StreamSites(context.Background(), siteCh2)

	s.Close()

	db := openDB(t, s)
	if got := rowCount(t, db, "sites"); got != 1 {
		t.Fatalf("expected 1 site, got %d", got)
	}
	if got := rowCount(t, db, "site_users"); got != 1 {
		t.Fatalf("expected 1 user (rollback should prevent user 2), got %d", got)
	}
}

// ---------------------------------------------------------------------------
// NDJSON Failure Log
// ---------------------------------------------------------------------------

func TestFailedFlush_WritesModelObjectsToNDJSON(t *testing.T) {
	dir := t.TempDir()
	ndjsonPath := filepath.Join(dir, "failures.ndjson")

	s, err := NewSQLiteStorage(Config{
		Path:             dir,
		Name:             "test.db",
		Recreate:         true, // INSERT mode
		BatchSize:        100,
		MaxConns:         1,
		EnableFailureLog: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Override the failure log path to our temp dir
	ss := s.(*sqliteStorage)
	ss.queue.EnableFailureJSON(ndjsonPath, true)

	// Stream a site and a duplicate site -- the duplicate triggers a PK violation
	// which rolls back the flush and writes the failed batch to the NDJSON file.
	ch := make(chan model.Site, 2)
	ch <- testSite("s1")
	ch <- testSite("s1") // PK violation
	close(ch)
	s.StreamSites(context.Background(), ch)
	s.Close()

	data, err := os.ReadFile(ndjsonPath)
	if err != nil {
		t.Fatalf("failed to read NDJSON file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 NDJSON line, got %d", len(lines))
	}

	var raw struct {
		BatchSize int               `json:"batchSize"`
		Error     string            `json:"error"`
		Items     []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &raw); err != nil {
		t.Fatalf("failed to parse NDJSON line: %v", err)
	}

	if raw.BatchSize != 2 {
		t.Fatalf("BatchSize = %d, want 2", raw.BatchSize)
	}
	if raw.Error == "" {
		t.Fatal("Error should not be empty")
	}
	if len(raw.Items) != 2 {
		t.Fatalf("Items length = %d, want 2", len(raw.Items))
	}

	// Verify the items are full model.Site JSON objects
	var site model.Site
	if err := json.Unmarshal(raw.Items[0], &site); err != nil {
		t.Fatalf("failed to parse first item as Site: %v", err)
	}
	if site.SiteId != "s1" {
		t.Fatalf("first item SiteId = %q, want %q", site.SiteId, "s1")
	}
	if site.SiteUrl != "https://contoso.sharepoint.com/sites/s1" {
		t.Fatalf("first item SiteUrl = %q, want contoso URL", site.SiteUrl)
	}
}

// ---------------------------------------------------------------------------
// Upsert Mode
// ---------------------------------------------------------------------------

func TestStreamSites_UpsertOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Path:      dir,
		Name:      "test.db",
		Recreate:  false, // keep existing DB so the second pass hits the same rows
		BatchSize: 100,
		MaxConns:  1,
	}

	// First pass: insert site with title "v1".
	s1, err := NewSQLiteStorage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ch1 := make(chan model.Site, 1)
	site := testSite("upsert-id")
	site.Title = "v1"
	ch1 <- site
	close(ch1)
	s1.StreamSites(context.Background(), ch1)
	s1.Close()

	// Second pass: same site_id, title "v2".
	s2, err := NewSQLiteStorage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ch2 := make(chan model.Site, 1)
	site.Title = "v2"
	ch2 <- site
	close(ch2)
	s2.StreamSites(context.Background(), ch2)
	s2.Close()

	db := openDB(t, s2)
	if got := rowCount(t, db, "sites"); got != 1 {
		t.Fatalf("expected 1 site after upsert, got %d", got)
	}

	var title string
	if err := db.QueryRow("SELECT title FROM sites WHERE site_id = ?", "upsert-id").Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "v2" {
		t.Fatalf("expected title 'v2', got %q", title)
	}
}

// ---------------------------------------------------------------------------
// SaveOutcomes
// ---------------------------------------------------------------------------

func TestSaveSiteOutcomes_PersistsToDatabase(t *testing.T) {
	s := newTestStorage(t)
	outcomes := []model.SPSiteOutcome{
		{SiteURL: "https://a.sharepoint.com", SiteID: "id-a", Timestamp: time.Now(), UsersFetched: 10},
		{SiteURL: "https://b.sharepoint.com", SiteID: "id-b", Timestamp: time.Now(), GroupsFetched: 5},
	}
	if err := s.SaveSiteOutcomes(outcomes); err != nil {
		t.Fatalf("SaveSiteOutcomes: %v", err)
	}

	db := rawDB(t, s)
	if got := rowCount(t, db, "site_outcomes"); got != 2 {
		t.Fatalf("expected 2, got %d", got)
	}

	var processor, jsonStr string
	err := db.QueryRow("SELECT processor, outcome_json FROM site_outcomes WHERE site_id = ?", "id-a").
		Scan(&processor, &jsonStr)
	if err != nil {
		t.Fatal(err)
	}
	if processor != "sharepoint" {
		t.Fatalf("processor = %q, want 'sharepoint'", processor)
	}
	var parsed model.SPSiteOutcome
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("unmarshal outcome: %v", err)
	}
	if parsed.UsersFetched != 10 {
		t.Fatalf("UsersFetched = %d, want 10", parsed.UsersFetched)
	}
}

func TestSaveOneDriveOutcomes_PersistsToDatabase(t *testing.T) {
	s := newTestStorage(t)
	outcomes := []model.ODSiteOutcome{
		{SiteURL: "https://od.sharepoint.com", SiteID: "od-1", Timestamp: time.Now()},
	}
	if err := s.SaveOneDriveOutcomes(outcomes); err != nil {
		t.Fatalf("SaveOneDriveOutcomes: %v", err)
	}

	db := rawDB(t, s)
	if got := rowCount(t, db, "site_outcomes"); got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}

	var processor string
	err := db.QueryRow("SELECT processor FROM site_outcomes WHERE site_id = ?", "od-1").Scan(&processor)
	if err != nil {
		t.Fatal(err)
	}
	if processor != "onedrive" {
		t.Fatalf("processor = %q, want 'onedrive'", processor)
	}
}

func TestSaveSiteOutcomes_ReplacesOnDuplicate(t *testing.T) {
	s := newTestStorage(t)
	o := model.SPSiteOutcome{SiteURL: "https://a.sharepoint.com", SiteID: "dup", Timestamp: time.Now(), UsersFetched: 1}
	if err := s.SaveSiteOutcomes([]model.SPSiteOutcome{o}); err != nil {
		t.Fatal(err)
	}
	o.UsersFetched = 99
	if err := s.SaveSiteOutcomes([]model.SPSiteOutcome{o}); err != nil {
		t.Fatal(err)
	}

	db := rawDB(t, s)
	if got := rowCount(t, db, "site_outcomes"); got != 1 {
		t.Fatalf("expected 1 after replace, got %d", got)
	}

	var jsonStr string
	if err := db.QueryRow("SELECT outcome_json FROM site_outcomes WHERE site_id = ?", "dup").Scan(&jsonStr); err != nil {
		t.Fatal(err)
	}
	var parsed model.SPSiteOutcome
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.UsersFetched != 99 {
		t.Fatalf("UsersFetched = %d, want 99", parsed.UsersFetched)
	}
}

// ---------------------------------------------------------------------------
// Run Status
// ---------------------------------------------------------------------------

func TestRunStatus_InitializeAndComplete(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	if err := s.InitializeRunStatus(ctx); err != nil {
		t.Fatalf("InitializeRunStatus: %v", err)
	}
	if err := s.MarkRunCompleted(ctx); err != nil {
		t.Fatalf("MarkRunCompleted: %v", err)
	}

	db := rawDB(t, s)
	var completed bool
	err := db.QueryRow("SELECT full_export_completed FROM run_status WHERE id = 1").Scan(&completed)
	if err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("expected full_export_completed = true")
	}
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

func TestGetStats_ReflectsStreamedItems(t *testing.T) {
	s := newTestStorage(t)

	const n = 5
	ch := make(chan model.Site, n)
	for i := range n {
		ch <- testSite(fmt.Sprintf("stat-%d", i))
	}
	close(ch)

	s.StreamSites(context.Background(), ch)
	s.Close() // stats must survive Close

	stats := s.GetStats()
	if stats.ProcessedItems != n {
		t.Fatalf("ProcessedItems = %d, want %d", stats.ProcessedItems, n)
	}
	if stats.BatchesWritten < 1 {
		t.Fatalf("BatchesWritten = %d, want >= 1", stats.BatchesWritten)
	}
}

// ---------------------------------------------------------------------------
// Archive
// ---------------------------------------------------------------------------

func TestArchiveDatabase_RenamesFile(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLiteStorage(Config{
		Path:      dir,
		Name:      "test.db",
		Recreate:  true,
		BatchSize: 100,
		MaxConns:  1,
	})
	if err != nil {
		t.Fatal(err)
	}

	archiver, ok := s.(Archiver)
	if !ok {
		t.Fatal("storage does not implement Archiver")
	}
	if err := archiver.ArchiveDatabase(); err != nil {
		t.Fatalf("ArchiveDatabase: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "test.db")); !os.IsNotExist(err) {
		t.Fatal("original DB should not exist after archive")
	}
	if _, err := os.Stat(filepath.Join(dir, "sharepoint_export_complete.db")); err != nil {
		t.Fatalf("archive file should exist: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

func TestNewStorage_SQLite(t *testing.T) {
	s, err := NewStorage(Config{
		Type:      "sqlite",
		Path:      t.TempDir(),
		Name:      "test.db",
		Recreate:  true,
		BatchSize: 100,
		MaxConns:  1,
	})
	if err != nil {
		t.Fatalf("NewStorage(sqlite): %v", err)
	}
	defer s.Close()
	if err := s.HealthCheck(); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
}

func TestNewStorage_EmptyType(t *testing.T) {
	s, err := NewStorage(Config{
		Type:      "",
		Path:      t.TempDir(),
		Name:      "test.db",
		Recreate:  true,
		BatchSize: 100,
		MaxConns:  1,
	})
	if err != nil {
		t.Fatalf("NewStorage(''): %v", err)
	}
	defer s.Close()
	if err := s.HealthCheck(); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
}

func TestNewStorage_UnsupportedType(t *testing.T) {
	_, err := NewStorage(Config{
		Type:      "postgres",
		Path:      t.TempDir(),
		Name:      "test.db",
		Recreate:  true,
		BatchSize: 100,
		MaxConns:  1,
	})
	if err == nil {
		t.Fatal("expected error for unsupported type")
	}
}
