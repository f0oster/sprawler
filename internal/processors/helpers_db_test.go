package processors

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"sprawler/internal/storage"
)

type testStorageResult struct {
	storage.Storage
	dbPath string
}

func newTestStorage(t *testing.T) *testStorageResult {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.NewSQLiteStorage(storage.Config{
		Path:          dir,
		Name:          "test.db",
		Recreate:      true,
		BatchSize:     100,
		FlushInterval: 0,
		MaxConns:      1,
	})
	if err != nil {
		t.Fatalf("NewSQLiteStorage: %v", err)
	}
	return &testStorageResult{Storage: s, dbPath: dir + "/test.db"}
}

func openTestDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func queryCount(t *testing.T, db *sql.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("queryCount(%s): %v", table, err)
	}
	return n
}

func assertQueryInt(t *testing.T, db *sql.DB, want int64, query string, args ...any) {
	t.Helper()
	var got int64
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if got != want {
		t.Fatalf("query %q = %d, want %d", query, got, want)
	}
}
