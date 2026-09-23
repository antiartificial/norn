package store

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

// ControlExportFormatVersion is the version of the canonical export envelope.
// It is independent of SchemaVersion (the database shape): the export format can
// evolve without a schema change and vice versa.
const ControlExportFormatVersion = 1

// controlExportAuditLimit bounds a single export read of the audit log. If the
// export hits it, Truncated is set so a caller never mistakes a capped export
// for a complete one.
const controlExportAuditLimit = 100000

// ControlExport is the canonical, versioned snapshot of the security-critical
// control state for inspection and disaster recovery: durable mutation-audit
// evidence (with its original record digests preserved) and the auth aggregate
// (devices, their tokens, IP grants and enrollments). Records carry their stable
// identifiers so an import can reconcile by id. This is the backup/export format
// the M1 gate calls for; broader per-boundary coverage is additive to the same
// envelope.
type ControlExport struct {
	FormatVersion  int                              `json:"formatVersion"`
	SchemaVersion  int                              `json:"schemaVersion"`
	ExportedAt     time.Time                        `json:"exportedAt"`
	Truncated      bool                             `json:"truncated"`
	AuditEvents    []ExportedAuditEvent             `json:"auditEvents"`
	AuditIncidents map[string]MutationAuditIncident `json:"auditIncidents"`
	Devices        []AccessDevice                   `json:"devices"`
	Grants         []AccessGrant                    `json:"grants"`
	Enrollments    []AccessEnrollment               `json:"enrollments"`
}

// ExportedAuditEvent surfaces the RecordDigest that MutationAuditEvent tags
// json:"-": disaster recovery must preserve the original receipt/signature bytes,
// so the export records them explicitly rather than dropping them.
type ExportedAuditEvent struct {
	MutationAuditEvent
	RecordDigest string `json:"recordDigest"`
}

// ExportControlState assembles the canonical export at the given timestamp
// (passed in for deterministic, reproducible exports). Slices are sorted by
// stable id so the same state produces the same document.
func (db *DB) ExportControlState(ctx context.Context, exportedAt time.Time) (*ControlExport, error) {
	export := &ControlExport{
		FormatVersion:  ControlExportFormatVersion,
		SchemaVersion:  SchemaVersion,
		ExportedAt:     exportedAt.UTC(),
		AuditIncidents: map[string]MutationAuditIncident{},
	}

	audits, err := db.ListMutationAudits(ctx, controlExportAuditLimit)
	if err != nil {
		return nil, err
	}
	if len(audits) >= controlExportAuditLimit {
		export.Truncated = true
	}
	export.AuditEvents = make([]ExportedAuditEvent, len(audits))
	for i, a := range audits {
		export.AuditEvents[i] = ExportedAuditEvent{MutationAuditEvent: a, RecordDigest: a.RecordDigest}
	}
	if len(audits) > 0 {
		ids := make([]string, len(audits))
		for i, a := range audits {
			ids[i] = a.ID
		}
		incidents, err := db.ListMutationAuditIncidents(ctx, ids)
		if err != nil {
			return nil, err
		}
		export.AuditIncidents = incidents
	}

	devices, err := db.ListAccessDevices(ctx)
	if err != nil {
		return nil, err
	}
	export.Devices = devices

	grants, err := db.ListAccessGrants(ctx)
	if err != nil {
		return nil, err
	}
	export.Grants = grants

	enrollments, err := db.ListAccessEnrollments(ctx, "")
	if err != nil {
		return nil, err
	}
	export.Enrollments = enrollments

	sort.Slice(export.AuditEvents, func(i, j int) bool { return export.AuditEvents[i].ID < export.AuditEvents[j].ID })
	sort.Slice(export.Devices, func(i, j int) bool { return export.Devices[i].ID < export.Devices[j].ID })
	sort.Slice(export.Grants, func(i, j int) bool { return export.Grants[i].ID < export.Grants[j].ID })
	sort.Slice(export.Enrollments, func(i, j int) bool { return export.Enrollments[i].ID < export.Enrollments[j].ID })
	return export, nil
}

// MarshalCanonical renders the export as stable, indented JSON — the on-disk
// backup form.
func (e *ControlExport) MarshalCanonical() ([]byte, error) {
	return json.MarshalIndent(e, "", "  ")
}
