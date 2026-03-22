package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"sprawler/internal/batchwriter"
	sqlcdb "sprawler/internal/database/sqlc"
	"sprawler/internal/logger"
	"sprawler/internal/model"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema/001_schema.sql
var schemaSQL string

type sqliteStorage struct {
	db        *sql.DB
	queries   *sqlcdb.Queries
	dbPath    string
	basePath  string
	queue     *batchwriter.BatchWriter
	logger    *logger.Logger
	closeOnce sync.Once
}

// NewSQLiteStorage creates a SQLite-backed [Storage] implementation.
func NewSQLiteStorage(cfg Config) (Storage, error) {
	dbPath := filepath.Join(cfg.Path, cfg.Name)

	if err := os.MkdirAll(cfg.Path, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	if cfg.Recreate {
		if _, err := os.Stat(dbPath); err == nil {
			if err := os.Remove(dbPath); err != nil {
				return nil, fmt.Errorf("failed to remove existing database: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("error checking existing database: %w", err)
		}
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxConns)
	db.SetMaxIdleConns(cfg.MaxConns)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	if err := configurePragmas(db, cfg.BusyTimeout, cfg.CacheSize); err != nil {
		return nil, fmt.Errorf("failed to configure database pragmas: %w", err)
	}

	queries := sqlcdb.New(db)

	queue := batchwriter.New(
		context.WithoutCancel(context.Background()),
		cfg.BatchSize,
		cfg.FlushInterval,
		cfg.QueueCapacity,
	)

	storage := &sqliteStorage{
		db:       db,
		queries:  queries,
		dbPath:   dbPath,
		basePath: cfg.Path,
		queue:    queue,
		logger:   logger.NewLogger("SQLiteStorage"),
	}

	if err := storage.migrate(); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	storage.registerBatchOperations(cfg.Recreate)

	if cfg.EnableFailureLog {
		path := cfg.FailureLogPath
		if path == "" {
			path = "./failed_writes.ndjson"
		}
		queue.EnableFailureJSON(path, true)
	}

	return storage, nil
}

// registerBatchOperations registers all SQLC-based batch operations with the queue
func (s *sqliteStorage) registerBatchOperations(recreate bool) {
	// Register SQLC operations - use INSERTs when database is recreated, UPSERTs otherwise
	registerSQLCOperations(s.queue, s.db, s.queries, recreate)
}

// GetStats returns current storage statistics
func (s *sqliteStorage) GetStats() model.StorageStats {
	successful, failed, transactions := s.queue.GetStats()
	return model.StorageStats{
		ProcessedItems: successful,
		FailedItems:    failed,
		BatchesWritten: transactions,
		QueueLength:    s.queue.QueueLength(),
	}
}

// streamTo reads values from ch and sends them to the queue with the given kind,
// stopping when the channel closes or the context is cancelled.
func streamTo[T any](queue *batchwriter.BatchWriter, ctx context.Context, ch <-chan T, kind batchwriter.Kind[T]) {
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return
			}
			batchwriter.Enqueue(queue, kind, v)
		case <-ctx.Done():
			return
		}
	}
}

func (s *sqliteStorage) StreamSites(ctx context.Context, sites <-chan model.Site) {
	streamTo(s.queue, ctx, sites, KindSites)
}

func (s *sqliteStorage) StreamUsers(ctx context.Context, users <-chan model.SiteUser) {
	streamTo(s.queue, ctx, users, KindSiteUsers)
}

func (s *sqliteStorage) StreamProfiles(ctx context.Context, profiles <-chan model.UserProfile) {
	streamTo(s.queue, ctx, profiles, KindUserProfiles)
}

func (s *sqliteStorage) StreamGroups(ctx context.Context, groups <-chan model.SiteGroup) {
	streamTo(s.queue, ctx, groups, KindSiteGroups)
}

func (s *sqliteStorage) StreamMembers(ctx context.Context, members <-chan model.GroupMember) {
	streamTo(s.queue, ctx, members, KindGroupMembers)
}

// SaveSiteOutcomes persists SharePoint site outcome records to the database.
func (s *sqliteStorage) SaveSiteOutcomes(outcomes []model.SPSiteOutcome) error {
	items := make([]outcomeItem, len(outcomes))
	for i, o := range outcomes {
		items[i] = outcomeItem{siteURL: o.SiteURL, siteID: o.SiteID, value: o}
	}
	return s.saveOutcomes("sharepoint", items)
}

// SaveOneDriveOutcomes persists OneDrive site outcome records to the database.
func (s *sqliteStorage) SaveOneDriveOutcomes(outcomes []model.ODSiteOutcome) error {
	items := make([]outcomeItem, len(outcomes))
	for i, o := range outcomes {
		items[i] = outcomeItem{siteURL: o.SiteURL, siteID: o.SiteID, value: o}
	}
	return s.saveOutcomes("onedrive", items)
}

type outcomeItem struct {
	siteURL string
	siteID  string
	value   any
}

func (s *sqliteStorage) saveOutcomes(processor string, items []outcomeItem) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO site_outcomes (site_url, site_id, processor, outcome_json) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, item := range items {
		jsonBytes, err := json.Marshal(item.value)
		if err != nil {
			s.logger.Errorf("Failed to marshal outcome for site %s: %v", item.siteURL, err)
			continue
		}
		if _, err := stmt.Exec(item.siteURL, item.siteID, processor, string(jsonBytes)); err != nil {
			s.logger.Errorf("Failed to save outcome for site %s: %v", item.siteURL, err)
		}
	}

	return tx.Commit()
}

func (s *sqliteStorage) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		// Close writer to flush pending items
		s.queue.Close()

		// Close database connection
		closeErr = s.db.Close()
	})
	return closeErr
}

// ArchiveDatabase closes current database, removes old archive, and renames current to archive
func (s *sqliteStorage) ArchiveDatabase() error {
	// Close current database connection first
	if err := s.Close(); err != nil {
		return fmt.Errorf("failed to close database before archiving: %w", err)
	}

	// Generate archive filename
	archivePath := filepath.Join(s.basePath, "sharepoint_export_complete.db")

	// Remove old archive if it exists
	if _, err := os.Stat(archivePath); err == nil {
		if err := os.Remove(archivePath); err != nil {
			return fmt.Errorf("failed to remove old archive: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("error checking archive file: %w", err)
	}

	// Rename current database to archive
	if err := os.Rename(s.dbPath, archivePath); err != nil {
		return fmt.Errorf("failed to rename database to archive: %w", err)
	}

	return nil
}

// HealthCheck verifies storage connectivity
func (s *sqliteStorage) HealthCheck() error {
	return s.db.Ping()
}

// configurePragmas sets all SQLite pragmas for the connection.
func configurePragmas(db *sql.DB, busyTimeout, cacheSize int) error {
	if busyTimeout <= 0 {
		busyTimeout = 5000
	}
	if cacheSize == 0 {
		cacheSize = -262144
	}

	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = OFF",
		fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeout),
		fmt.Sprintf("PRAGMA cache_size = %d", cacheSize),
		"PRAGMA temp_store = MEMORY",
		"PRAGMA mmap_size = 268435456",
		"PRAGMA wal_autocheckpoint = 4000",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("failed to set pragma %s: %w", pragma, err)
		}
	}
	return nil
}

// migrate runs database schema migrations
func (s *sqliteStorage) migrate() error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}
	return nil
}

// InitializeRunStatus creates initial run status record
func (s *sqliteStorage) InitializeRunStatus(ctx context.Context) error {
	err := s.queries.CreateRunStatus(ctx, sqlcdb.CreateRunStatusParams{
		ID:                  1,
		FullExportCompleted: false,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize run status: %w", err)
	}
	s.logger.Debugf("Run status initialized")
	return nil
}

// MarkRunCompleted marks the enumeration run as completed
func (s *sqliteStorage) MarkRunCompleted(ctx context.Context) error {
	err := s.queries.UpdateRunStatus(ctx, sqlcdb.UpdateRunStatusParams{
		ID:                  1,
		FullExportCompleted: true,
	})
	if err != nil {
		return fmt.Errorf("failed to mark run completed: %w", err)
	}
	s.logger.Debugf("Run status marked as completed")
	return nil
}
