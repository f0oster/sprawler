// Sprawler extracts sites, users, groups, and OneDrive profiles from SharePoint.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"sprawler/internal/api"
	"sprawler/internal/config"
	"sprawler/internal/logger"
	"sprawler/internal/processors"
	"sprawler/internal/storage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := logger.NewLogger("Main")
	logger.Info("Starting Sprawler SharePoint Enumeration Tool")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			logger.Info("Interrupt received, draining pipeline (Ctrl+C again to force quit)")
			stop()
			// Second signal: force quit
			<-sigCh
			os.Exit(130)
		case <-ctx.Done():
		}
	}()

	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize storage
	storageConfig := storage.Config{
		Type:     cfg.Database.Type,
		Path:     cfg.Database.Path,
		Name:     cfg.Database.Name,
		MaxConns: cfg.Database.MaxConns,
		Recreate: cfg.Database.Recreate,

		BatchSize:        cfg.Database.BatchSize,
		FlushInterval:    cfg.Database.FlushInterval,
		QueueCapacity:    cfg.Database.QueueCapacity,
		EnableFailureLog: cfg.Database.EnableFailureLog,
		FailureLogPath:   cfg.Database.FailureLogPath,

		BusyTimeout: cfg.Database.BusyTimeout,
		CacheSize:   cfg.Database.CacheSize,
	}

	store, err := storage.NewStorage(storageConfig)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	defer store.Close()

	// Create per-processor API clients so each has isolated metrics
	spClient, err := api.NewClient(cfg.Auth, cfg.Transport)
	if err != nil {
		log.Fatalf("Failed to create SharePoint API client: %v", err)
	}
	odClient, err := api.NewClient(cfg.Auth, cfg.Transport)
	if err != nil {
		log.Fatalf("Failed to create OneDrive API client: %v", err)
	}

	if err := store.InitializeRunStatus(ctx); err != nil {
		log.Fatalf("Failed to initialize run status: %v", err)
	}

	// Health checks
	processorList := []processors.Processor{
		processors.NewSharePointProcessor(cfg, store, spClient),
		processors.NewOneDriveProcessor(cfg, store, odClient),
	}

	for _, p := range processorList {
		if err := p.Health(ctx); err != nil {
			log.Fatalf("Health check failed for %s: %v", p.Name(), err)
		}
	}

	// Run processors
	for i, p := range processorList {
		if ctx.Err() != nil {
			logger.Info("Skipping remaining processors due to interrupt")
			break
		}
		logger.Infof("Running processor %d/%d: %s", i+1, len(processorList), p.Name())
		result := p.Process(ctx)
		logger.Infof("%s: %s", result.ProcessorName, result.Summary)

		if len(result.SPOutcomes) > 0 {
			logger.Warnf("  %d SharePoint site outcomes recorded", len(result.SPOutcomes))
		}
		if len(result.ODOutcomes) > 0 {
			logger.Warnf("  %d OneDrive site outcomes recorded", len(result.ODOutcomes))
		}
	}

	// Finalize
	if ctx.Err() == nil {
		if err := store.MarkRunCompleted(ctx); err != nil {
			logger.Warnf("Failed to mark completion in database: %v", err)
		}

		if archiver, ok := store.(storage.Archiver); ok {
			logger.Info("Archiving database")
			if err := archiver.ArchiveDatabase(); err != nil {
				logger.Warnf("Failed to archive database: %v", err)
			}
		}

		// Close store explicitly so DB stats print before "completed"
		store.Close()

		logger.Info("SharePoint enumeration completed successfully!")
	} else {
		logger.Info("Run interrupted, skipping finalization")
	}
}
