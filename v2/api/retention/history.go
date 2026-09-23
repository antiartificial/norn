package retention

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/archive"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

// HistoryStore is an archive-aware saga.Store (reader contract 2). Appends
// go to the hot store. Every read merges pruned archive bundles, each
// verified against its recorded object identity: ListBySaga returns the
// complete saga; ListByApp and ListRecent return the true newest events
// across hot and archived history. A needed bundle that cannot be read,
// including on a server without an archive configured, fails the read
// explicitly rather than returning a silently hot-only or partial answer.
type HistoryStore struct {
	Hot     saga.Store
	DB      *store.DB
	Archive archive.Store
}

func (h *HistoryStore) Append(ctx context.Context, event *saga.Event) error {
	return h.Hot.Append(ctx, event)
}

func (h *HistoryStore) ListByApp(ctx context.Context, app string, limit int) ([]saga.Event, error) {
	hot, err := h.Hot.ListByApp(ctx, app, limit)
	if err != nil {
		return nil, err
	}
	return h.newestWithArchive(ctx, app, limit, hot)
}

func (h *HistoryStore) ListRecent(ctx context.Context, limit int) ([]saga.Event, error) {
	hot, err := h.Hot.ListRecent(ctx, limit)
	if err != nil {
		return nil, err
	}
	return h.newestWithArchive(ctx, "", limit, hot)
}

// newestWithArchive merges pruned bundles (newest cutoff first) into the
// hot newest-first listing. Once at least limit events are held, a bundle
// whose newest event is older than the current limit-th event cannot
// contribute, and nor can any later bundle, so the scan stops; every bundle
// that could contribute is read and verified.
func (h *HistoryStore) newestWithArchive(ctx context.Context, app string, limit int, hot []saga.Event) ([]saga.Event, error) {
	if h.DB == nil || limit <= 0 {
		return hot, nil
	}
	merged := map[string]saga.Event{}
	for _, event := range hot {
		merged[event.ID] = event
	}
	events := sortNewestFirst(merged)
	const page = 50
	for offset := 0; ; offset += page {
		intents, err := h.DB.PrunedEvidenceIntents(ctx, app, offset, page)
		if err != nil {
			return nil, err
		}
		for _, intent := range intents {
			if len(events) >= limit && intent.CutoffTimestamp != nil && intent.CutoffTimestamp.Before(events[limit-1].Timestamp) {
				return events[:limit], nil
			}
			if h.Archive == nil {
				return nil, &ErrArchivedHistoryUnavailable{IntentID: intent.ID, Err: errors.New("this server has no evidence archive configured")}
			}
			bundle, err := LoadBundle(ctx, h.Archive, intent)
			if err != nil {
				return nil, &ErrArchivedHistoryUnavailable{IntentID: intent.ID, Err: err}
			}
			for _, event := range bundle.Events {
				if _, ok := merged[event.ID]; !ok {
					merged[event.ID] = event
				}
			}
			events = sortNewestFirst(merged)
		}
		if len(intents) < page {
			break
		}
	}
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

func sortNewestFirst(byID map[string]saga.Event) []saga.Event {
	events := make([]saga.Event, 0, len(byID))
	for _, event := range byID {
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].Timestamp.Equal(events[j].Timestamp) {
			return events[i].ID < events[j].ID
		}
		return events[i].Timestamp.After(events[j].Timestamp)
	})
	return events
}

// ErrArchivedHistoryUnavailable reports pruned history that cannot be read.
type ErrArchivedHistoryUnavailable struct {
	IntentID string
	Err      error
}

func (e *ErrArchivedHistoryUnavailable) Error() string {
	return fmt.Sprintf("archived history %s is unavailable: %v", e.IntentID, e.Err)
}

func (e *ErrArchivedHistoryUnavailable) Unwrap() error { return e.Err }

func (h *HistoryStore) ListBySaga(ctx context.Context, sagaID string) ([]saga.Event, error) {
	hot, err := h.Hot.ListBySaga(ctx, sagaID)
	if err != nil {
		return nil, err
	}
	if h.DB == nil {
		return hot, nil
	}
	intents, err := h.DB.EvidenceIntentsForSubject(ctx, "saga", sagaID)
	if err != nil {
		return nil, err
	}
	merged := map[string]saga.Event{}
	for _, event := range hot {
		merged[event.ID] = event
	}
	for _, intent := range intents {
		if intent.State != "pruned" {
			continue
		}
		if h.Archive == nil {
			return nil, &ErrArchivedHistoryUnavailable{IntentID: intent.ID, Err: errors.New("this server has no evidence archive configured")}
		}
		bundle, err := LoadBundle(ctx, h.Archive, intent)
		if err != nil {
			return nil, &ErrArchivedHistoryUnavailable{IntentID: intent.ID, Err: err}
		}
		for _, event := range bundle.Events {
			if _, ok := merged[event.ID]; !ok {
				merged[event.ID] = event
			}
		}
	}
	events := make([]saga.Event, 0, len(merged))
	for _, event := range merged {
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].Timestamp.Equal(events[j].Timestamp) {
			return events[i].ID < events[j].ID
		}
		return events[i].Timestamp.Before(events[j].Timestamp)
	})
	return events, nil
}

// SagaApp returns the app a saga belongs to, from hot events or the
// evidence index (for fully pruned sagas); "" when unknown.
func (h *HistoryStore) SagaApp(ctx context.Context, sagaID string) (string, error) {
	var app string
	err := h.DB.Pool.QueryRow(ctx, `
		SELECT app FROM (
			SELECT app FROM saga_events WHERE saga_id = $1
			UNION ALL SELECT app FROM evidence_archive_intents WHERE subject_kind = 'saga' AND subject_id = $1
			UNION ALL SELECT app FROM operations WHERE saga_id = $1
		) owners WHERE app <> '' LIMIT 1`, sagaID).Scan(&app)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	return app, nil
}

// IndexRecovery reports a RestoreIndex run.
type IndexRecovery struct {
	Objects  int      `json:"objects"`
	Restored int      `json:"restored"`
	Existing int      `json:"existing"`
	Rejected []string `json:"rejected,omitempty"`
}

// RestoreIndex rebuilds the evidence index from the archive alone (for a
// control store restored without its historical rows): every bundle object
// is read, verified for internal consistency (and its acceptance signature
// when a signer is given) and recorded from its own validated subject. This
// includes saga history and terminal operation receipts. Existing index rows
// are never overwritten.
func RestoreIndex(ctx context.Context, db *store.DB, objects archive.Reader, signer store.AcceptanceSigner) (IndexRecovery, error) {
	var recovery IndexRecovery
	keys, err := objects.List(ctx, "evidence/")
	if err != nil {
		return recovery, err
	}
	verifier := &Archiver{Signer: signer}
	for _, key := range keys {
		recovery.Objects++
		bundle, info, err := verifier.verifiedBundleAt(ctx, objects, key)
		if err != nil {
			recovery.Rejected = append(recovery.Rejected, key+": "+err.Error())
			continue
		}
		var cutoff *time.Time
		if bundle.Cutoff.EventCount > 0 {
			value := bundle.Cutoff.LastTimestamp
			cutoff = &value
		}
		restored, err := db.RestoreEvidenceIntent(ctx, store.EvidenceIntent{
			ID: "ei-restored-" + info.SHA256[:24], SubjectKind: bundle.Subject.Kind, SubjectID: bundle.Subject.ID, App: bundle.Subject.App,
			OperationID: bundle.Subject.OperationID, Sequence: bundle.Sequence, EventIDs: bundle.Cutoff.EventIDs, CutoffTimestamp: cutoff,
			ObjectKey: info.Key, ObjectSHA256: info.SHA256, ObjectBytes: info.Size,
		})
		if err != nil {
			return recovery, err
		}
		if restored {
			recovery.Restored++
		} else {
			recovery.Existing++
		}
	}
	return recovery, nil
}

// verifiedBundleAt reads one archived object (bounded), decodes and
// validates the bundle, requires the key derived from its subject, and binds
// (and, with a signer, verifies) its signed acceptance.
func (a *Archiver) verifiedBundleAt(ctx context.Context, objects archive.Reader, key string) (*archive.Bundle, archive.ObjectInfo, error) {
	data, info, err := objects.Get(ctx, key, archive.MaxBundleBytes)
	if err != nil {
		return nil, info, err
	}
	bundle, err := archive.Open(data)
	if err != nil {
		return nil, info, err
	}
	if expected, err := archive.ObjectKey(bundle.Subject, bundle.Sequence); err != nil || expected != key {
		return nil, info, fmt.Errorf("object key does not match its bundle subject")
	}
	if err := a.verifyAcceptance(ctx, bundle); err != nil {
		return nil, info, err
	}
	return bundle, info, nil
}

// ArchiveVerification reports an offline whole-archive verification.
type ArchiveVerification struct {
	Objects            int      `json:"objects"`
	Verified           int      `json:"verified"`
	Events             int      `json:"events"`
	Bytes              int64    `json:"bytes"`
	SignaturesVerified bool     `json:"signaturesVerified"`
	Rejected           []string `json:"rejected,omitempty"`
}

// VerifyArchive reads and verifies every evidence object without any
// control database: bounded reads, bundle integrity, subject-derived keys
// and acceptance content binding, plus signatures when a signer is given.
func VerifyArchive(ctx context.Context, objects archive.Reader, signer store.AcceptanceSigner) (ArchiveVerification, error) {
	report := ArchiveVerification{SignaturesVerified: signer != nil}
	keys, err := objects.List(ctx, "evidence/")
	if err != nil {
		return report, err
	}
	verifier := &Archiver{Signer: signer}
	for _, key := range keys {
		report.Objects++
		bundle, info, err := verifier.verifiedBundleAt(ctx, objects, key)
		if err != nil {
			report.Rejected = append(report.Rejected, key+": "+err.Error())
			continue
		}
		report.Verified++
		report.Events += len(bundle.Events)
		report.Bytes += info.Size
	}
	return report, nil
}
