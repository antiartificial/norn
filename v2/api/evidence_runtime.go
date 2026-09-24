package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"norn/v2/api/archive"
	"norn/v2/api/config"
	"norn/v2/api/retention"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

// evidenceArchiveSettings maps configuration to archive settings. The
// backend is explicit: "local" is the consolidated local/Mini profile,
// "object" the Fleet profile. A directory alone selects local for
// compatibility; object storage is never inferred.
func evidenceArchiveSettings(cfg *config.Config) (archive.Settings, bool) {
	backend := cfg.EvidenceArchiveBackend
	if backend == "" && cfg.EvidenceArchiveDir != "" {
		backend = "local"
	}
	if backend == "" && cfg.EvidenceArchiveEndpoint == "" && cfg.EvidenceArchiveBucket == "" {
		return archive.Settings{}, false
	}
	return archive.Settings{Backend: backend, Dir: cfg.EvidenceArchiveDir, MaxBytes: cfg.EvidenceArchiveMaxBytes,
		Endpoint: cfg.EvidenceArchiveEndpoint, Bucket: cfg.EvidenceArchiveBucket, Prefix: cfg.EvidenceArchivePrefix, Region: cfg.EvidenceArchiveRegion,
		AccessKeyFile: cfg.EvidenceArchiveAccessKeyFile, SecretKeyFile: cfg.EvidenceArchiveSecretKeyFile, CAFile: cfg.EvidenceArchiveCAFile,
		ApplicationAccessKey: cfg.S3AccessKey}, true
}

// configureEvidenceArchive returns the saga store handlers should read and
// the archiver. Reads are always archive-aware (reader contract 2): without
// an archive there is no archiver, and history another server pruned is
// refused rather than served partially. Misconfiguration fails startup
// rather than running without an archive the operator asked for.
func configureEvidenceArchive(cfg *config.Config, db *store.DB, hot saga.Store) (saga.Store, *retention.Archiver, error) {
	settings, configured := evidenceArchiveSettings(cfg)
	if !configured {
		if db == nil {
			return hot, nil, nil
		}
		return &retention.HistoryStore{Hot: hot, DB: db}, nil, nil
	}
	mode := retention.Mode(cfg.EvidenceArchiveMode)
	if mode != retention.ModeShadow && mode != retention.ModePrune {
		return nil, nil, fmt.Errorf("NORN_EVIDENCE_ARCHIVE_MODE must be shadow or prune")
	}
	minAge, err := time.ParseDuration(cfg.EvidenceMinAge)
	if err != nil || minAge < 0 {
		return nil, nil, fmt.Errorf("NORN_EVIDENCE_MIN_AGE must be a non-negative duration")
	}
	if settings.Backend == "local" && cfg.EvidenceArchiveMaxBytes <= 0 {
		return nil, nil, fmt.Errorf("NORN_EVIDENCE_ARCHIVE_MAX_BYTES must be a positive byte count")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	objects, _, err := archive.OpenStore(ctx, settings)
	if err != nil {
		return nil, nil, fmt.Errorf("evidence archive (%s backend): %w", settings.Backend, err)
	}
	var signer store.AcceptanceSigner
	if cfg.AuditSigningKey != "" {
		if hmac, err := store.NewHMACAcceptanceSigner(cfg.AuditSigningKey, cfg.AuditPreviousSigningKeys...); err == nil {
			signer = hmac
		}
	}
	archiver := &retention.Archiver{DB: db, Archive: objects, Signer: signer, Mode: mode, Quiet: 2 * time.Minute, BackfillAfter: 10 * time.Minute, MinAge: minAge, BatchSize: 50,
		MinFreeBytes: cfg.EvidenceReserveMinFreeBytes}
	return &retention.HistoryStore{Hot: hot, DB: db, Archive: objects}, archiver, nil
}

// applyEvidenceReservePolicy records the durable reserve policy. A process
// with an archive enables it; only an explicit NORN_EVIDENCE_RESERVE=disabled
// disables it. A process without an archive leaves the policy unchanged, so
// dropping archive configuration cannot silently lift admission control.
func applyEvidenceReservePolicy(ctx context.Context, cfg *config.Config, db *store.DB, archiveConfigured bool) error {
	switch cfg.EvidenceReserve {
	case "enforce":
		if !archiveConfigured {
			return nil
		}
	case "disabled":
	default:
		return fmt.Errorf("NORN_EVIDENCE_RESERVE must be enforce or disabled")
	}
	return db.SetEvidenceReservePolicy(ctx, store.EvidenceReservePolicy{Enabled: cfg.EvidenceReserve == "enforce",
		MaxPending: cfg.EvidenceReserveMaxPending, MaxPendingAge: cfg.EvidenceReserveMaxPendingAge,
		MaxSignedAcceptanceBytes: cfg.EvidenceReserveSignedBytes})
}

// runEvidenceArchiver runs bounded archive passes until ctx ends. Failures
// keep evidence pending and hot; they are logged without payloads.
func runEvidenceArchiver(ctx context.Context, archiver *retention.Archiver, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		report, err := archiver.RunOnce(ctx)
		if err != nil {
			log.Printf("evidence archive pass failed: %v", err)
		} else if report.Published > 0 || report.Pruned > 0 || len(report.PublishErrors) > 0 || len(report.ShadowMismatch) > 0 {
			log.Printf("evidence archive: published=%d pruned=%d held=%d errors=%d shadowMismatches=%d", report.Published, report.Pruned, report.Held, len(report.PublishErrors), len(report.ShadowMismatch))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
