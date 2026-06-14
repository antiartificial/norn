package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/storage"
)

type snapshotEntry struct {
	Filename  string `json:"filename"`
	Database  string `json:"database"`
	CommitSHA string `json:"commitSha,omitempty"`
	Timestamp string `json:"timestamp"`
	CreatedAt string `json:"createdAt,omitempty"`
	Size      int64  `json:"size"`
}

type restoreReceipt struct {
	Status             string         `json:"status"`
	App                string         `json:"app"`
	Database           string         `json:"database"`
	Snapshot           snapshotEntry  `json:"snapshot"`
	PreRestoreSnapshot *snapshotEntry `json:"preRestoreSnapshot,omitempty"`
	RestoredAt         string         `json:"restoredAt"`
}

type snapshotRetentionReceipt struct {
	Status     string          `json:"status"`
	App        string          `json:"app"`
	Keep       int             `json:"keep"`
	DryRun     bool            `json:"dryRun"`
	Kept       []snapshotEntry `json:"kept"`
	Pruned     []snapshotEntry `json:"pruned"`
	WouldPrune []snapshotEntry `json:"wouldPrune,omitempty"`
	AppliedAt  string          `json:"appliedAt"`
}

func (h *Handler) ListSnapshots(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	if spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
		writeJSON(w, []snapshotEntry{})
		return
	}

	writeJSON(w, listSnapshotsForSpec(spec))
}

func listSnapshotsForSpec(spec *model.InfraSpec) []snapshotEntry {
	if spec == nil || spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
		return []snapshotEntry{}
	}
	dbName := spec.Infrastructure.Postgres.Database
	entries, err := os.ReadDir("snapshots")
	if err != nil {
		return []snapshotEntry{}
	}

	var snapshots []snapshotEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, dbName+"_") || !strings.HasSuffix(name, ".dump") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}

		snapshot := parseSnapshotEntry(dbName, name, info.Size())
		if snapshot == nil {
			continue
		}

		snapshots = append(snapshots, *snapshot)
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Timestamp > snapshots[j].Timestamp
	})

	return snapshots
}

func (h *Handler) RestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ts := chi.URLParam(r, "ts")

	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	if spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
		writeError(w, http.StatusBadRequest, "app has no postgres database")
		return
	}

	dbName := spec.Infrastructure.Postgres.Database
	if r.URL.Query().Get("confirm") != "true" {
		writeError(w, http.StatusBadRequest, "restore requires confirm=true")
		return
	}

	// Find matching snapshot file
	entries, err := os.ReadDir("snapshots")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot read snapshots directory")
		return
	}

	var match *snapshotEntry
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, dbName+"_") && strings.Contains(name, ts) && strings.HasSuffix(name, ".dump") {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			parsed := parseSnapshotEntry(dbName, name, info.Size())
			if parsed != nil {
				match = parsed
			}
			break
		}
	}

	if match == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("no snapshot found for timestamp %s", ts))
		return
	}

	snapshotPath := filepath.Join("snapshots", match.Filename)
	var preRestore *snapshotEntry
	preRestoreRequested := r.URL.Query().Get("preRestore") == "true" || (spec.Snapshots != nil && spec.Snapshots.PreRestore)
	if preRestoreRequested {
		created, err := createSnapshotForSpec(r.Context(), spec, "pre-restore")
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("pre-restore snapshot: %v", err))
			return
		}
		preRestore = created
	}

	cmd := exec.CommandContext(r.Context(), "pg_restore", "--clean", "-d", dbName, snapshotPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// pg_restore returns warnings on --clean even on success
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() > 1 {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("pg_restore: %s", string(out)))
			return
		}
	}

	if h.ws != nil {
		h.ws.Broadcast(hub.Event{
			Type:  "snapshot.restored",
			AppID: id,
			Payload: map[string]string{
				"database":  dbName,
				"snapshot":  match.Filename,
				"timestamp": ts,
			},
		})
	}
	if h.beacon != nil {
		metadata := map[string]interface{}{
			"database":  dbName,
			"snapshot":  match.Filename,
			"timestamp": ts,
		}
		if preRestore != nil {
			metadata["preRestoreSnapshot"] = preRestore.Filename
		}
		h.emitSnapshotEvent(r, id, "snapshot.restored", model.BeaconWarning, "snapshot restored", fmt.Sprintf("%s restored snapshot %s", id, match.Filename), metadata)
	}

	writeJSON(w, restoreReceipt{
		Status:             "restored",
		App:                id,
		Database:           dbName,
		Snapshot:           *match,
		PreRestoreSnapshot: preRestore,
		RestoredAt:         timeNowUTC(),
	})
}

func (h *Handler) ApplySnapshotRetention(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	keep := queryIntDefault(r, "keep", snapshotKeepForSpec(spec, 3))
	if keep < 1 {
		writeError(w, http.StatusBadRequest, "keep must be at least 1")
		return
	}
	confirm := r.URL.Query().Get("confirm") == "true"
	snapshots := listSnapshotsForSpec(spec)
	receipt := snapshotRetentionReceipt{
		Status:    "preview",
		App:       id,
		Keep:      keep,
		DryRun:    !confirm,
		AppliedAt: timeNowUTC(),
	}
	for i, snapshot := range snapshots {
		if i < keep {
			receipt.Kept = append(receipt.Kept, snapshot)
			continue
		}
		if !confirm {
			receipt.WouldPrune = append(receipt.WouldPrune, snapshot)
			continue
		}
		if err := os.Remove(filepath.Join("snapshots", snapshot.Filename)); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("prune %s: %v", snapshot.Filename, err))
			return
		}
		receipt.Pruned = append(receipt.Pruned, snapshot)
	}
	if confirm {
		receipt.Status = "applied"
		if h.ws != nil {
			h.ws.Broadcast(hub.Event{
				Type:  "snapshot.retention",
				AppID: id,
				Payload: map[string]string{
					"keep":   fmt.Sprintf("%d", keep),
					"pruned": fmt.Sprintf("%d", len(receipt.Pruned)),
				},
			})
		}
		h.emitSnapshotEvent(r, id, "snapshot.retention.applied", model.BeaconInfo, "snapshot retention applied", fmt.Sprintf("%s pruned %d snapshot(s)", id, len(receipt.Pruned)), map[string]interface{}{
			"keep":   keep,
			"pruned": len(receipt.Pruned),
		})
	}
	writeJSON(w, receipt)
}

func createSnapshotForSpec(ctx context.Context, spec *model.InfraSpec, commitSHA string) (*snapshotEntry, error) {
	if spec == nil || spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
		return nil, fmt.Errorf("app has no postgres database")
	}
	dbName := spec.Infrastructure.Postgres.Database
	sha := commitSHA
	if sha == "" {
		sha = "manual"
	}
	if len(sha) > 12 {
		sha = sha[:12]
	}
	timestamp := time.Now().UTC().Format("20060102T150405")
	filename := fmt.Sprintf("%s_%s_%s.dump", dbName, sha, timestamp)
	path := filepath.Join("snapshots", filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create snapshots dir: %w", err)
	}
	cmd := exec.CommandContext(ctx, "pg_dump", "-Fc", "-d", dbName, "-f", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("pg_dump: %s", string(out))
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return parseSnapshotEntry(dbName, filename, info.Size()), nil
}

func (h *Handler) emitSnapshotEvent(r *http.Request, app, eventType string, severity model.BeaconSeverity, title, body string, metadata map[string]interface{}) {
	if h.beacon == nil {
		return
	}
	_, _ = h.beacon.Emit(r.Context(), model.BeaconEvent{
		App:       app,
		Type:      eventType,
		Severity:  severity,
		Title:     title,
		Body:      body,
		DedupeKey: app + ":" + eventType,
		Metadata:  metadata,
	})
}

func parseSnapshotEntry(dbName, filename string, size int64) *snapshotEntry {
	if !strings.HasPrefix(filename, dbName+"_") || !strings.HasSuffix(filename, ".dump") {
		return nil
	}
	stem := strings.TrimSuffix(filename, ".dump")
	timestampSep := strings.LastIndex(stem, "_")
	if timestampSep < 0 || timestampSep == len(stem)-1 {
		return nil
	}
	prefix := stem[:timestampSep]
	timestamp := stem[timestampSep+1:]
	shaSep := strings.LastIndex(prefix, "_")
	if shaSep < 0 || shaSep == len(prefix)-1 {
		return nil
	}
	database := prefix[:shaSep]
	if database != dbName {
		return nil
	}
	commitSHA := prefix[shaSep+1:]
	return &snapshotEntry{
		Filename:  filename,
		Database:  database,
		CommitSHA: commitSHA,
		Timestamp: timestamp,
		CreatedAt: snapshotTimestampRFC3339(timestamp),
		Size:      size,
	}
}

func snapshotTimestampRFC3339(ts string) string {
	t, err := time.Parse("20060102T150405", ts)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func timeNowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func (h *Handler) ExportSnapshot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	if spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
		writeError(w, http.StatusBadRequest, "app has no postgres database")
		return
	}
	if h.s3 == nil {
		writeError(w, http.StatusBadRequest, "object storage not configured")
		return
	}
	exportBucket := ""
	if spec.Snapshots != nil {
		exportBucket = spec.Snapshots.ExportBucket
	}
	if exportBucket == "" {
		writeError(w, http.StatusBadRequest, "no export bucket configured")
		return
	}

	snapshots := listSnapshotsForSpec(spec)
	if len(snapshots) == 0 {
		writeError(w, http.StatusNotFound, "no local snapshots available")
		return
	}
	snapshot := snapshots[0] // latest

	key := "snapshots/" + id + "/" + snapshot.Filename
	localPath := filepath.Join("snapshots", snapshot.Filename)
	if err := h.s3.PutObject(r.Context(), exportBucket, key, localPath); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("upload snapshot: %v", err))
		return
	}

	h.emitSnapshotEvent(r, id, "snapshot.exported", model.BeaconInfo, "snapshot exported",
		fmt.Sprintf("%s exported snapshot %s to %s", id, snapshot.Filename, exportBucket),
		map[string]interface{}{
			"bucket":   exportBucket,
			"key":      key,
			"snapshot": snapshot.Filename,
		})

	writeJSON(w, map[string]interface{}{
		"status":   "exported",
		"app":      id,
		"snapshot": snapshot,
		"bucket":   exportBucket,
		"key":      key,
	})
}

func (h *Handler) ListRemoteSnapshots(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	if h.s3 == nil {
		writeError(w, http.StatusBadRequest, "object storage not configured")
		return
	}
	exportBucket := ""
	if spec.Snapshots != nil {
		exportBucket = spec.Snapshots.ExportBucket
	}
	if exportBucket == "" {
		writeError(w, http.StatusBadRequest, "no export bucket configured")
		return
	}

	objects, err := h.s3.ListObjects(r.Context(), exportBucket, "snapshots/"+id+"/")
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("list remote snapshots: %v", err))
		return
	}
	if objects == nil {
		objects = []storage.ObjectInfo{}
	}

	writeJSON(w, map[string]interface{}{
		"snapshots": objects,
	})
}

func (h *Handler) ImportSnapshot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	if h.s3 == nil {
		writeError(w, http.StatusBadRequest, "object storage not configured")
		return
	}
	exportBucket := ""
	if spec.Snapshots != nil {
		exportBucket = spec.Snapshots.ExportBucket
	}
	if exportBucket == "" {
		writeError(w, http.StatusBadRequest, "no export bucket configured")
		return
	}

	var req struct {
		Key string `json:"key"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	localPath := filepath.Join("snapshots", filepath.Base(req.Key))
	if err := os.MkdirAll("snapshots", 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("create snapshots dir: %v", err))
		return
	}
	if err := h.s3.GetObject(r.Context(), exportBucket, req.Key, localPath); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("download snapshot: %v", err))
		return
	}

	h.emitSnapshotEvent(r, id, "snapshot.imported", model.BeaconInfo, "snapshot imported",
		fmt.Sprintf("%s imported snapshot %s from %s", id, filepath.Base(req.Key), exportBucket),
		map[string]interface{}{
			"bucket":    exportBucket,
			"key":       req.Key,
			"localPath": "snapshots/" + filepath.Base(req.Key),
		})

	writeJSON(w, map[string]interface{}{
		"status":    "imported",
		"app":       id,
		"key":       req.Key,
		"localPath": "snapshots/" + filepath.Base(req.Key),
	})
}

func (h *Handler) findSpec(appID string) *model.InfraSpec {
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		return nil
	}
	for _, s := range specs {
		if s.App == appID {
			return s
		}
	}
	return nil
}
