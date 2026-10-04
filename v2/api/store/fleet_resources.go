package store

// This file is the PG half of WP10 (docs/v3/fleet-controller/plan.md §2.3,
// §3): the Fleet resource and its observation stream. It stores no second
// operation ledger (mirroring §2.2's fence "lock, not a ledger" rule):
// Status.Active (set by the caller's derive function, never here) only ever
// names plan, dispatch and attempt IDs that already live in
// fleet_target_fences, fleet_github_dispatches and fleet_runner_attempts.
//
// Table layout (store/fleet_resources_migration.go, migration 49), mirroring
// store/fleet_targets_migration.go's conventions:
//
//	fleet_resources(name PK, target_id FK, document JSONB, revision,
//	  authority_epoch, observation_sequence, created_at, updated_at)
//	fleet_observations(resource, sequence, source, observed_at, received_at,
//	  applied, facts, evidence_refs, reporter, PK(resource,sequence))
//
// document holds everything controller.Resource carries beyond the broken-
// out columns: SchemaVersion, Desired, DesiredHistory, Policy, Watermarks
// and Status.
//
// CAS. ReconcileFleetResource is the only writer of Status, Revision and
// AuthorityEpoch. In one REPEATABLE READ transaction (one snapshot for every
// input; 40001 retries) it locks the resource row FOR UPDATE, reads the epoch FOR
// SHARE, the fence FOR SHARE (lock order position 2-4, same as
// store/fleet_targets.go's header comment), the fence holder's dispatch and
// attempts, and every watermark's own observation row, all inside one
// transaction, calls the caller's derive function, and only then writes
// revision+1 and the freshly-read authority_epoch (never a value the caller
// cached earlier -- see TestFleetResourceOldEpochWriterRecomputesPostgres-
// style cases in the shared suite). AppendFleetObservation takes the same
// FOR UPDATE lock, so it always serializes against an in-flight reconcile
// (M9): a reconcile that is already past this lock sees a stable snapshot
// for its whole transaction, and a concurrent append must wait for it to
// commit or roll back.
//
// Monotonicity. AppendFleetObservation applies plan.md §2.3's rule
// (controller.ApplyObservation): Applied = ObservedAt > Watermarks[source].
// ObservedAt, plus up to MaxWatermarkTies exact ties so derive can let a tied
// failure win. An observation that
// does not advance its source's watermark is still stored (never rejected),
// just marked Applied=false; pruning never deletes the row a watermark
// names, so a flood on one source can never resurrect an older observation
// on another (M10).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/lifecycle"
)

// ErrFleetResourceNotFound is returned by any read or write that requires an
// existing resource.
var ErrFleetResourceNotFound = fmt.Errorf("fleet resource not found")

// ErrFleetResourceTargetMismatch is returned by EnsureFleetResource when the
// resource already exists under a different target: a resource's target
// binding is fixed at creation and nothing in this plan rebinds it.
var ErrFleetResourceTargetMismatch = fmt.Errorf("fleet resource is already bound to a different target")

// ErrFleetObservationOutOfBounds is returned by AppendFleetObservation for a
// Facts payload over MaxObservationFactsBytes, more than
// MaxObservationEvidenceRefs evidence refs, or an ObservedAt outside
// [ReceivedAt-24h, ReceivedAt+1m].
var ErrFleetObservationOutOfBounds = fmt.Errorf("fleet observation is out of bounds")

// ErrFleetResourceDesiredInvalid is returned by SetDesiredFleetResource for a
// DesiredRevision controller.ValidateDesired refuses, and by
// EnsureFleetResource for an unusable name.
var ErrFleetResourceDesiredInvalid = fmt.Errorf("fleet resource desired revision is invalid")

// ErrFleetResourceReconcileConflict is returned by ReconcileFleetResource
// once every attempt has hit a serialization failure (plan.md §2.3: "retry
// up to 3 times, then ErrStatusPreconditionFailed").
var ErrFleetResourceReconcileConflict = fmt.Errorf("fleet resource reconcile lost its serialization race after every retry")

const fleetResourceReconcileAttempts = 3

// fleetResourceDocument is the document JSONB column's shape: everything
// controller.Resource holds beyond the columns broken out onto fleet_resources
// itself.
type fleetResourceDocument struct {
	SchemaVersion  string                          `json:"schemaVersion"`
	Desired        controller.DesiredRevision      `json:"desired"`
	DesiredHistory []controller.DesiredRevision    `json:"desiredHistory,omitempty"`
	Policy         controller.ApprovalPolicy       `json:"policy"`
	Watermarks     map[string]controller.Watermark `json:"watermarks,omitempty"`
	Status         controller.Status               `json:"status"`
}

func fleetResourceDocumentOf(r controller.Resource) fleetResourceDocument {
	return fleetResourceDocument{
		SchemaVersion: r.SchemaVersion, Desired: r.Desired, DesiredHistory: r.DesiredHistory,
		Policy: r.Policy, Watermarks: r.Watermarks, Status: r.Status,
	}
}

func (d fleetResourceDocument) toResource(name, targetID string, revision, authorityEpoch, observationSequence int64, createdAt, updatedAt time.Time) controller.Resource {
	return controller.Resource{
		SchemaVersion: d.SchemaVersion, Name: name, TargetID: targetID,
		Desired: d.Desired, DesiredHistory: d.DesiredHistory, Policy: d.Policy, Watermarks: d.Watermarks, Status: d.Status,
		Revision: revision, AuthorityEpoch: authorityEpoch, ObservationSequence: observationSequence,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

// scanFleetResource reads one fleet_resources row.
func scanFleetResource(row pgx.Row) (*controller.Resource, error) {
	var name, targetID string
	var documentBytes []byte
	var revision, authorityEpoch, observationSequence int64
	var createdAt, updatedAt time.Time
	if err := row.Scan(&name, &targetID, &documentBytes, &revision, &authorityEpoch, &observationSequence, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	var document fleetResourceDocument
	if err := json.Unmarshal(documentBytes, &document); err != nil {
		return nil, fmt.Errorf("fleet resource %q document is corrupt: %w", name, err)
	}
	resource := document.toResource(name, targetID, revision, authorityEpoch, observationSequence, createdAt, updatedAt)
	return &resource, nil
}

const fleetResourceColumns = `name, target_id, document, revision, authority_epoch, observation_sequence, created_at, updated_at`

// EnsureFleetResource idempotently creates name's resource bound to
// targetID. Replaying the same targetID is a no-op; a different targetID is
// refused (ErrFleetResourceTargetMismatch), since a resource's target is
// fixed at creation.
func (db *DB) EnsureFleetResource(ctx context.Context, name, targetID string) (*controller.Resource, error) {
	if !controller.ValidResourceName(name) || targetID == "" {
		return nil, ErrFleetResourceDesiredInvalid
	}
	now := time.Now().UTC()
	document, err := json.Marshal(fleetResourceDocument{SchemaVersion: controller.SchemaVersion})
	if err != nil {
		return nil, err
	}
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO fleet_resources (name, target_id, document, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4)
		ON CONFLICT (name) DO NOTHING
	`, name, targetID, document, now)
	if err != nil {
		return nil, err
	}
	resource, err := db.GetFleetResource(ctx, name)
	if err != nil {
		return nil, err
	}
	if resource.TargetID != targetID {
		return nil, ErrFleetResourceTargetMismatch
	}
	return resource, nil
}

// GetFleetResource returns name's resource, or ErrFleetResourceNotFound.
func (db *DB) GetFleetResource(ctx context.Context, name string) (*controller.Resource, error) {
	resource, err := scanFleetResource(db.Pool.QueryRow(ctx, `SELECT `+fleetResourceColumns+` FROM fleet_resources WHERE name=$1`, name))
	if err == pgx.ErrNoRows {
		return nil, ErrFleetResourceNotFound
	}
	return resource, err
}

// SetDesiredFleetResource accepts a new desired revision (Q8): the caller
// resolves PlanID/CommitSHA/Verification before calling (github-merged-plan
// via ResolveApprovedPlan, or operator-declared). The previous Desired is
// pushed onto DesiredHistory (newest first), bounded to
// controller.DesiredHistoryLimit. It touches only this resource's own row:
// no fence, dispatch or attempt row is read or written
// (DesiredChangeDuringExecutionKeepsBindings).
func (db *DB) SetDesiredFleetResource(ctx context.Context, name string, next controller.DesiredRevision) (*controller.Resource, error) {
	if err := controller.ValidateDesired(next); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFleetResourceDesiredInvalid, err)
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	resource, err := scanFleetResource(tx.QueryRow(ctx, `SELECT `+fleetResourceColumns+` FROM fleet_resources WHERE name=$1 FOR UPDATE`, name))
	if err == pgx.ErrNoRows {
		return nil, ErrFleetResourceNotFound
	}
	if err != nil {
		return nil, err
	}

	if resource.Desired.Generation > 0 {
		resource.DesiredHistory = append([]controller.DesiredRevision{resource.Desired}, resource.DesiredHistory...)
	}
	if len(resource.DesiredHistory) > controller.DesiredHistoryLimit {
		resource.DesiredHistory = resource.DesiredHistory[:controller.DesiredHistoryLimit]
	}
	next.Generation = resource.Desired.Generation + 1
	resource.Desired = next
	now := time.Now().UTC()
	resource.UpdatedAt = now

	document, err := json.Marshal(fleetResourceDocumentOf(*resource))
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE fleet_resources SET document=$2, updated_at=$3 WHERE name=$1`, name, document, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return resource, nil
}

// ListFleetObservations returns up to limit observations for name, newest
// sequence first.
func (db *DB) ListFleetObservations(ctx context.Context, name string, limit int) ([]controller.Observation, error) {
	if limit <= 0 || limit > controller.ObservationLimit {
		limit = controller.ObservationLimit
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT sequence, source, observed_at, received_at, applied, facts, evidence_refs, reporter
		FROM fleet_observations WHERE resource=$1 ORDER BY sequence DESC LIMIT $2
	`, name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []controller.Observation{}
	for rows.Next() {
		obs, err := scanFleetObservationRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, obs)
	}
	return out, rows.Err()
}

type fleetObservationScanner interface {
	Scan(dest ...interface{}) error
}

func scanFleetObservationRow(row fleetObservationScanner) (controller.Observation, error) {
	var obs controller.Observation
	var factsBytes, evidenceBytes []byte
	if err := row.Scan(&obs.Sequence, &obs.Source, &obs.ObservedAt, &obs.ReceivedAt, &obs.Applied, &factsBytes, &evidenceBytes, &obs.Reporter); err != nil {
		return controller.Observation{}, err
	}
	if err := json.Unmarshal(factsBytes, &obs.Facts); err != nil {
		return controller.Observation{}, fmt.Errorf("fleet observation facts are corrupt: %w", err)
	}
	if err := json.Unmarshal(evidenceBytes, &obs.EvidenceRefs); err != nil {
		return controller.Observation{}, fmt.Errorf("fleet observation evidence refs are corrupt: %w", err)
	}
	return obs, nil
}

// AppendFleetObservation ingests one observation for name. The server
// assigns Sequence and ReceivedAt; the caller supplies Source, ObservedAt,
// Facts, EvidenceRefs and Reporter. ObservedAt must fall in
// [ReceivedAt-24h, ReceivedAt+1m] or the call is refused
// (ErrFleetObservationOutOfBounds) and nothing is stored or pruned.
//
// Applied = ObservedAt > the source's current watermark ObservedAt (plan.md
// §2.3): an observation that does not advance its watermark is still stored,
// just marked unapplied, so an older report can never clear a newer failure
// (M10). Appending this way, inside the same FOR UPDATE transaction as the
// resource document write, is also what lets ReconcileFleetResource's own
// FOR UPDATE serialize against it (M9).
func (db *DB) AppendFleetObservation(ctx context.Context, name string, source string, observedAt time.Time, facts controller.ObservationFacts, evidenceRefs []string, reporter string) (*controller.Resource, controller.Observation, error) {
	if !controller.ValidSource(source) || !controller.ValidateObservationStrings(evidenceRefs, reporter) {
		return nil, controller.Observation{}, ErrFleetObservationOutOfBounds
	}
	if facts == nil {
		facts = controller.ObservationFacts{}
	}
	if evidenceRefs == nil {
		evidenceRefs = []string{}
	}
	// Truncate to the column's microsecond precision so the stored row and
	// the watermark compare identically on every later append.
	observedAt = observedAt.UTC().Truncate(time.Microsecond)
	factsBytes, err := json.Marshal(facts)
	if err != nil {
		return nil, controller.Observation{}, err
	}
	if len(factsBytes) > controller.MaxObservationFactsBytes {
		return nil, controller.Observation{}, ErrFleetObservationOutOfBounds
	}
	receivedAt := time.Now().UTC().Truncate(time.Microsecond)
	if !controller.ObservationWithinBounds(observedAt, receivedAt) {
		return nil, controller.Observation{}, ErrFleetObservationOutOfBounds
	}
	evidenceBytes, err := json.Marshal(evidenceRefs)
	if err != nil {
		return nil, controller.Observation{}, err
	}

	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, controller.Observation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	resource, err := scanFleetResource(tx.QueryRow(ctx, `SELECT `+fleetResourceColumns+` FROM fleet_resources WHERE name=$1 FOR UPDATE`, name))
	if err == pgx.ErrNoRows {
		return nil, controller.Observation{}, ErrFleetResourceNotFound
	}
	if err != nil {
		return nil, controller.Observation{}, err
	}

	sequence := resource.ObservationSequence + 1
	wm, hasWatermark := resource.Watermarks[source]
	applied, nextWatermark := controller.ApplyObservation(wm, hasWatermark, observedAt, sequence)

	if _, err := tx.Exec(ctx, `
		INSERT INTO fleet_observations (resource, sequence, source, observed_at, received_at, applied, facts, evidence_refs, reporter)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, name, sequence, source, observedAt, receivedAt, applied, factsBytes, evidenceBytes, reporter); err != nil {
		return nil, controller.Observation{}, err
	}

	resource.ObservationSequence = sequence
	if applied {
		if resource.Watermarks == nil {
			resource.Watermarks = map[string]controller.Watermark{}
		}
		resource.Watermarks[source] = nextWatermark
	}
	resource.UpdatedAt = receivedAt
	document, err := json.Marshal(fleetResourceDocumentOf(*resource))
	if err != nil {
		return nil, controller.Observation{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE fleet_resources SET document=$2, observation_sequence=$3, updated_at=$4 WHERE name=$1`,
		name, document, sequence, receivedAt); err != nil {
		return nil, controller.Observation{}, err
	}

	if err := pruneFleetObservationsTx(ctx, tx, name, resource.Watermarks); err != nil {
		return nil, controller.Observation{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, controller.Observation{}, err
	}
	obs := controller.Observation{Sequence: sequence, Source: source, ObservedAt: observedAt, ReceivedAt: receivedAt, Applied: applied, Facts: facts, EvidenceRefs: evidenceRefs, Reporter: reporter}
	return resource, obs, nil
}

// pruneFleetObservationsTx deletes the oldest rows for resource once its
// count exceeds controller.ObservationLimit, never a row any current
// watermark names (M10: a noisy source can never cause another source's
// newest applied observation to be pruned and later "reappear" as the
// latest).
func pruneFleetObservationsTx(ctx context.Context, tx pgx.Tx, resource string, watermarks map[string]controller.Watermark) error {
	var count int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM fleet_observations WHERE resource=$1`, resource).Scan(&count); err != nil {
		return err
	}
	excess := count - controller.ObservationLimit
	if excess <= 0 {
		return nil
	}
	keepSequences := make([]int64, 0, len(watermarks))
	for _, wm := range watermarks {
		keepSequences = append(keepSequences, wm.Sequences()...)
	}
	sort.Slice(keepSequences, func(i, j int) bool { return keepSequences[i] < keepSequences[j] })
	_, err := tx.Exec(ctx, `
		DELETE FROM fleet_observations WHERE resource=$1 AND sequence IN (
			SELECT sequence FROM fleet_observations WHERE resource=$1 AND NOT (sequence = ANY($2::bigint[]))
			ORDER BY sequence ASC LIMIT $3
		)
	`, resource, keepSequences, excess)
	return err
}

// ReconcileFleetResource gathers name's resource, the current authority
// epoch, the target fence (if any) and its holder's dispatch/attempts, and
// every watermark's own observation, all inside one transaction (plan.md
// §2.3's lock order), then calls derive and writes the result with
// revision+1 and the freshly read authority_epoch.
//
// afterRead, when non-nil, runs once, synchronously, after every input above
// has been read but before derive is called -- it exists only so the shared
// conformance suite can inject a concurrent write at exactly that point
// (ReconcileWriteSeesLatestInputs, M9). Production callers (WP12) pass nil.
// Because every read above holds the resource row FOR UPDATE for the whole
// transaction, a concurrent AppendFleetObservation (which takes the same
// lock) cannot interleave: it blocks until this transaction commits or rolls
// back, so PG's version of M9 is "serializes", never "sees a torn read".
func (db *DB) ReconcileFleetResource(ctx context.Context, name string, derive func(controller.Input) controller.Status, afterRead func()) (*controller.Resource, error) {
	for attempt := 0; attempt < fleetResourceReconcileAttempts; attempt++ {
		hook := afterRead
		if attempt > 0 {
			hook = nil
		}
		resource, err := db.reconcileFleetResourceOnce(ctx, name, derive, hook)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "40001" {
			continue
		}
		return resource, err
	}
	return nil, ErrFleetResourceReconcileConflict
}

// reconcileFleetResourceOnce runs one REPEATABLE READ attempt. READ
// COMMITTED would give each statement its own snapshot, so the fence, the
// holder's dispatch and attempts and the watermark rows could each come from
// a different commit (the resource FOR UPDATE lock does not freeze them) and
// derive could see a mix no real state ever had. REPEATABLE READ gives every
// read one snapshot; a row locked here (resource, epoch, fence) that another
// transaction changed after that snapshot fails with 40001 and the caller
// re-reads from scratch.
func (db *DB) reconcileFleetResourceOnce(ctx context.Context, name string, derive func(controller.Input) controller.Status, afterRead func()) (*controller.Resource, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	resource, err := scanFleetResource(tx.QueryRow(ctx, `SELECT `+fleetResourceColumns+` FROM fleet_resources WHERE name=$1 FOR UPDATE`, name))
	if err == pgx.ErrNoRows {
		return nil, ErrFleetResourceNotFound
	}
	if err != nil {
		return nil, err
	}

	epoch, err := FleetAuthorityEpoch(ctx, tx)
	if err != nil {
		return nil, err
	}

	var fence lifecycle.FenceFacts
	var holder lifecycle.HolderFacts
	if resource.TargetID != "" {
		fence, err = GetFleetTargetFence(ctx, tx, resource.TargetID, false)
		if err != nil {
			return nil, err
		}
		if fence.Held {
			holder, err = readFleetTargetHolderFactsTx(ctx, tx, fence.HolderPlanID)
			if err != nil {
				return nil, err
			}
		}
	}

	latest := map[string]controller.Observation{}
	tied := map[string][]controller.Observation{}
	for source, wm := range resource.Watermarks {
		for index, sequence := range wm.Sequences() {
			obs, err := scanFleetObservationRow(tx.QueryRow(ctx, `
				SELECT sequence, source, observed_at, received_at, applied, facts, evidence_refs, reporter
				FROM fleet_observations WHERE resource=$1 AND source=$2 AND sequence=$3
			`, name, source, sequence))
			if err != nil {
				return nil, fmt.Errorf("fleet resource %q watermark for source %q has no observation row %d: %w", name, source, sequence, err)
			}
			if index == 0 {
				latest[source] = obs
			} else {
				tied[source] = append(tied[source], obs)
			}
		}
	}

	if afterRead != nil {
		afterRead()
	}

	now := time.Now().UTC()
	status := derive(controller.Input{Resource: *resource, Fence: fence, Holder: holder, AuthorityEpoch: epoch, LatestObservations: latest, TiedObservations: tied, Now: now})

	resource.Status = status
	resource.Revision++
	resource.AuthorityEpoch = epoch
	resource.UpdatedAt = now

	document, err := json.Marshal(fleetResourceDocumentOf(*resource))
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE fleet_resources SET document=$2, revision=$3, authority_epoch=$4, updated_at=$5 WHERE name=$1`,
		name, document, resource.Revision, resource.AuthorityEpoch, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return resource, nil
}

// readFleetTargetHolderFactsTx re-derives HolderFacts for planID inside tx:
// its dispatch state/timestamps and its runner attempts, projected through
// lifecycle.FromLegacy exactly as the PG legacy lifecycle helpers already
// do. It writes nothing (T5: reads never write fence or attempt state) --
// unlike ListFleetRunnerAttempts, it never abandons a stale attempt as a
// side effect; expiry is only ever projected in memory by the caller's
// derive function via lifecycle.ProjectExpiry.
func readFleetTargetHolderFactsTx(ctx context.Context, tx pgx.Tx, planID string) (lifecycle.HolderFacts, error) {
	var holder lifecycle.HolderFacts
	dispatch, err := scanFleetGitHubDispatch(tx.QueryRow(ctx, `SELECT `+fleetGitHubDispatchColumns+` FROM fleet_github_dispatches WHERE plan_id=$1`, planID))
	if err != nil && err != pgx.ErrNoRows {
		return holder, err
	}
	if dispatch != nil {
		holder.DispatchState = dispatch.DispatchState
		holder.DispatchCreatedAt = dispatch.CreatedAt
		if dispatch.SubmissionStartedAt != nil {
			holder.SubmissionStartedAt = *dispatch.SubmissionStartedAt
		}
	}
	var abandoned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fleet_target_abandoned_plans WHERE plan_id=$1)`, planID).Scan(&abandoned); err != nil {
		return holder, err
	}
	holder.Abandoned = abandoned

	rows, err := tx.Query(ctx, `
		SELECT id, plan_id, attempt, root_attempt_id, source_dispatch_run_id, pilot_run_id, recovery, runner_attempt_id, status, current_phase,
		       commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
		       heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at, phase_started_at,
		       heartbeat_at, updated_at, finished_at, last_error, metadata
		FROM fleet_runner_attempts WHERE plan_id=$1 ORDER BY attempt ASC
	`, planID)
	if err != nil {
		return holder, err
	}
	defer rows.Close()
	for rows.Next() {
		attempt, err := scanFleetRunnerAttempt(rows)
		if err != nil {
			return holder, err
		}
		holder.Attempts = append(holder.Attempts, lifecycle.FromLegacy(attempt))
	}
	if err := rows.Err(); err != nil {
		return holder, err
	}
	return holder, nil
}
