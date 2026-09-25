package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/storage"
)

type snapshotEntry struct {
	Filename  string `json:"filename"`
	Database  string `json:"database"`
	CommitSHA string `json:"commitSha,omitempty"`
	Timestamp string `json:"timestamp"`
	CreatedAt string `json:"createdAt,omitempty"`
	Size      int64  `json:"size"`
	// Target-aware fields, present when a database profile is configured.
	LogicalDatabase   string `json:"logicalDatabase,omitempty"`
	BindingID         string `json:"bindingId,omitempty"`
	BindingGeneration uint64 `json:"bindingGeneration,omitempty"`
	Provenance        string `json:"provenance,omitempty"`
}

// databaseLabel names an app's databases for reports: the v1 database name,
// or the logical names of a v2 app.
func databaseLabel(spec *model.InfraSpec) string {
	if spec.NamedDatabases() {
		names := make([]string, 0, len(spec.Databases))
		for _, requirement := range spec.Databases {
			names = append(names, requirement.Name)
		}
		return strings.Join(names, ",")
	}
	if spec.Infrastructure != nil && spec.Infrastructure.Postgres != nil {
		return spec.Infrastructure.Postgres.Database
	}
	return ""
}

// snapshotsForSpec is an app's restorable snapshot inventory. With a
// database profile it comes from the target-aware layer restore uses, so it
// only lists dumps whose provenance names the app's current target; without
// one it is the unchanged v2 listing. Named databases are never listed by
// database name alone.
func (h *Handler) snapshotsForSpec(ctx context.Context, spec *model.InfraSpec) []snapshotEntry {
	entries, err := h.snapshotsForSpecResult(ctx, spec)
	if err != nil {
		return []snapshotEntry{}
	}
	return entries
}

func (h *Handler) snapshotsForSpecResult(ctx context.Context, spec *model.InfraSpec) ([]snapshotEntry, error) {
	if h.pipeline == nil || h.pipeline.DatabaseTargets == nil {
		if spec.NamedDatabases() {
			return nil, fmt.Errorf("named database snapshot targets are unavailable")
		}
		return listSnapshotsForSpec(spec), nil
	}
	groups, err := h.pipeline.TargetSnapshots(ctx, spec)
	if err != nil {
		return nil, err
	}
	out := []snapshotEntry{}
	for _, group := range groups {
		for _, snapshot := range group.Snapshots {
			entry := parseSnapshotEntry(group.DatabaseName, snapshot.Filename, snapshot.Size)
			if entry == nil {
				continue
			}
			entry.LogicalDatabase, entry.BindingID, entry.BindingGeneration, entry.Provenance = group.Database, group.BindingID, group.BindingGeneration, snapshot.Provenance
			out = append(out, *entry)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Timestamp == out[j].Timestamp {
			return out[i].Filename < out[j].Filename
		}
		return out[i].Timestamp > out[j].Timestamp
	})
	return out, nil
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

var snapshotLabelSanitizer = regexp.MustCompile(`[^A-Za-z0-9.-]+`)

func (h *Handler) ListSnapshots(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	if !spec.DeclaresDatabase() {
		writeJSON(w, []snapshotEntry{})
		return
	}

	writeJSON(w, h.snapshotsForSpec(r.Context(), spec))
}

// ListAppSnapshotsV1 exposes the same inventory through the versioned control
// contract, including its standard problem response shape.
func (h *Handler) ListAppSnapshotsV1(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	spec := h.findSpec(id)
	if spec == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", "app was not found or deployment is disabled")
		return
	}
	if !spec.DeclaresDatabase() {
		writeJSON(w, []snapshotEntry{})
		return
	}
	writeJSON(w, h.snapshotsForSpec(r.Context(), spec))
}

func listSnapshotsForSpec(spec *model.InfraSpec) []snapshotEntry {
	if spec == nil || spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
		return []snapshotEntry{}
	}
	dbName := spec.Infrastructure.Postgres.Database
	if !model.IsSafePostgresDatabaseName(dbName) {
		return []snapshotEntry{}
	}
	entries, err := os.ReadDir("snapshots")
	if err != nil {
		return []snapshotEntry{}
	}

	var snapshots []snapshotEntry
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
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
		if !info.Mode().IsRegular() {
			continue
		}

		snapshot := parseSnapshotEntry(dbName, name, info.Size())
		if snapshot == nil {
			continue
		}

		snapshots = append(snapshots, *snapshot)
	}

	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].Timestamp == snapshots[j].Timestamp {
			return snapshots[i].Filename < snapshots[j].Filename
		}
		return snapshots[i].Timestamp > snapshots[j].Timestamp
	})

	return snapshots
}

// importTargetSnapshot recovers an export into the current target's
// namespace. It never touches a database: the manifest must name this app's
// exact current target and the bytes must match it; restoring the imported
// dump is the ordinary durable restore operation.
func (h *Handler) importTargetSnapshot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	if h.s3 == nil || spec.Snapshots == nil || spec.Snapshots.ExportBucket == "" {
		writeError(w, http.StatusBadRequest, "object storage or export bucket not configured")
		return
	}
	var req struct {
		Key string `json:"key"`
	}
	if err := decodeJSON(r, &req); err != nil || !strings.HasPrefix(req.Key, "snapshots/"+id+"/") || strings.Contains(req.Key, "..") {
		writeError(w, http.StatusBadRequest, "key must name a snapshot in this app's export prefix")
		return
	}
	h.queueAppDataOperation(w, r, "app.snapshot-import", "snapshot import", map[string]interface{}{"bucket": spec.Snapshots.ExportBucket, "key": req.Key}, 1)
}

func (h *Handler) RestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	ts := chi.URLParam(r, "ts")
	if !snapshotTimestampPattern.MatchString(ts) {
		writeError(w, http.StatusBadRequest, "snapshot timestamp is invalid")
		return
	}
	if r.URL.Query().Get("confirm") != "true" {
		writeError(w, http.StatusBadRequest, "restore requires confirm=true")
		return
	}
	// The compatibility route shares the signed durable operation used by the
	// v1 control API. The worker validates snapshot ownership and always takes
	// a safety snapshot before restore; the API must never run pg_restore inline.
	h.queueAppDataOperation(w, r, "app.snapshot-restore", "destructive database restore with safety snapshot", map[string]interface{}{"snapshot": ts}, 1)
}

func (h *Handler) ApplySnapshotRetention(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	keep := queryIntDefault(r, "keep", snapshotKeepForSpec(spec, 3))
	if keep < 1 || keep > 1000 {
		writeError(w, http.StatusBadRequest, "keep must be between 1 and 1000")
		return
	}
	confirm := r.URL.Query().Get("confirm") == "true"
	if confirm {
		// File deletion belongs to the accepted worker operation. A repeated
		// compatibility request must resolve through the same idempotency key.
		h.queueAppDataOperation(w, r, "app.snapshot-prune", "destructive snapshot retention", map[string]interface{}{"keep": keep}, 1)
		return
	}
	selected := strings.TrimSpace(r.URL.Query().Get("database"))
	if spec.NamedDatabases() {
		if selected == "" && len(spec.Databases) == 1 {
			selected = spec.Databases[0].Name
		}
		requirement, declared := spec.DatabaseByName(selected)
		if !declared {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_database_selection", "database must name one of the app's declared databases")
			return
		}
		canSnapshot := false
		for _, capability := range requirement.Capabilities {
			canSnapshot = canSnapshot || capability == "snapshot"
		}
		if !canSnapshot {
			WriteControlProblem(w, r, http.StatusConflict, "snapshot_not_configured", "selected database does not declare snapshot capability")
			return
		}
	} else if selected != "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_database_selection", "database selection requires a named database")
		return
	}
	snapshots, err := h.snapshotsForSpecResult(r.Context(), spec)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "snapshot_inventory_unavailable", "snapshot inventory could not be verified for retention preview")
		return
	}
	if spec.NamedDatabases() {
		filtered := snapshots[:0]
		for _, snapshot := range snapshots {
			if snapshot.LogicalDatabase == selected {
				filtered = append(filtered, snapshot)
			}
		}
		snapshots = filtered
	}
	receipt := snapshotRetentionReceipt{
		Status:    "preview",
		App:       id,
		Keep:      keep,
		DryRun:    true,
		AppliedAt: timeNowUTC(),
	}
	for i, snapshot := range snapshots {
		if i < keep {
			receipt.Kept = append(receipt.Kept, snapshot)
			continue
		}
		receipt.WouldPrune = append(receipt.WouldPrune, snapshot)
	}
	writeJSON(w, receipt)
}

func createSnapshotForSpec(ctx context.Context, spec *model.InfraSpec, commitSHA string) (*snapshotEntry, error) {
	if spec == nil || spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
		return nil, fmt.Errorf("app has no postgres database")
	}
	dbName := spec.Infrastructure.Postgres.Database
	if !model.IsSafePostgresDatabaseName(dbName) {
		return nil, fmt.Errorf("app postgres database name is unsafe for snapshot storage")
	}
	sha := commitSHA
	if sha == "" {
		sha = "manual"
	}
	if len(sha) > 12 {
		sha = sha[:12]
	}
	sha = snapshotLabelSanitizer.ReplaceAllString(sha, "-")
	if sha == "" || sha == "." || sha == ".." {
		sha = "manual"
	}
	if err := os.MkdirAll("snapshots", 0o750); err != nil {
		return nil, fmt.Errorf("create snapshots dir: %w", err)
	}
	createdAt := time.Now().UTC()
	temporary, err := os.CreateTemp("snapshots", ".norn-snapshot-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("reserve snapshot file: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, fmt.Errorf("close snapshot reservation: %w", err)
	}
	defer os.Remove(temporaryPath)
	// #nosec G702 G204 -- no shell is used; dbName and the generated path are
	// restricted to the validated snapshot namespace.
	cmd := exec.CommandContext(ctx, "pg_dump", "-Fc", "-d", dbName, "-f", temporaryPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("pg_dump: %s", string(out))
	}
	info, err := os.Lstat(temporaryPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil, fmt.Errorf("pg_dump did not create a non-empty regular snapshot")
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return nil, fmt.Errorf("secure snapshot permissions: %w", err)
	}
	for offset := 0; offset < 1000; offset++ {
		timestamp := createdAt.Add(time.Duration(offset) * time.Second).Format("20060102T150405")
		filename := fmt.Sprintf("%s_%s_%s.dump", dbName, sha, timestamp)
		path := filepath.Join("snapshots", filename)
		if err := os.Link(temporaryPath, path); err == nil {
			return parseSnapshotEntry(dbName, filename, info.Size()), nil
		} else if !os.IsExist(err) {
			return nil, fmt.Errorf("publish snapshot: %w", err)
		}
	}
	return nil, fmt.Errorf("could not allocate a unique snapshot filename")
}

func (h *Handler) emitSnapshotEvent(r *http.Request, app, eventType string, severity model.BeaconSeverity, title, body string, metadata map[string]interface{}) {
	if h.beacon == nil {
		return
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	metadata["correlationKey"] = app + ":snapshots"
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
	if !snapshotTimestampPattern.MatchString(timestamp) {
		return nil
	}
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
	if !spec.DeclaresDatabase() {
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
	if h.pipeline != nil && h.pipeline.DatabaseTargets != nil {
		groups, err := h.pipeline.TargetSnapshots(r.Context(), spec)
		if err != nil {
			writeError(w, http.StatusConflict, fmt.Sprintf("snapshot inventory: %v", err))
			return
		}
		selected := r.URL.Query().Get("database")
		if selected == "" && len(groups) != 1 {
			writeError(w, http.StatusBadRequest, "database is required when multiple snapshot targets are declared")
			return
		}
		var group *pipeline.TargetSnapshotGroup
		for index := range groups {
			if selected == "" || groups[index].Database == selected {
				group = &groups[index]
				break
			}
		}
		if group == nil || group.Unavailable != "" {
			writeError(w, http.StatusConflict, "selected database snapshot inventory is unavailable")
			return
		}
		filename := r.URL.Query().Get("snapshot")
		if filename == "" && len(group.Snapshots) > 0 {
			filename = group.Snapshots[0].Filename
		}
		found := false
		for _, snapshot := range group.Snapshots {
			found = found || snapshot.Filename == filename
		}
		if !found {
			writeError(w, http.StatusNotFound, "no restorable snapshot of the selected target matches")
			return
		}
		h.queueAppDataOperation(w, r, "app.snapshot-export", "snapshot export", map[string]interface{}{"bucket": exportBucket, "snapshot": filename, "database": group.Database}, 1)
		return
	}
	if spec.NamedDatabases() {
		writeError(w, http.StatusConflict, "named databases require a database profile")
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
	if h.pipeline != nil && h.pipeline.DatabaseTargets != nil {
		h.importTargetSnapshot(w, r)
		return
	}
	id := chi.URLParam(r, "id")

	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	if spec.NamedDatabases() {
		writeError(w, http.StatusConflict, "named databases require a database profile")
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
	dbName := spec.Infrastructure.Postgres.Database
	if !model.IsSafePostgresDatabaseName(dbName) {
		writeError(w, http.StatusConflict, "app postgres database name is unsafe for snapshot storage")
		return
	}
	expectedPrefix := "snapshots/" + id + "/"
	filename := filepath.Base(req.Key)
	if !strings.HasPrefix(req.Key, expectedPrefix) || strings.Contains(strings.TrimPrefix(req.Key, expectedPrefix), "/") || parseSnapshotEntry(dbName, filename, 1) == nil {
		writeError(w, http.StatusBadRequest, "key must name a valid snapshot in this app's export prefix")
		return
	}

	h.queueAppDataOperation(w, r, "app.snapshot-import", "snapshot import", map[string]interface{}{"bucket": exportBucket, "key": req.Key}, 1)
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
