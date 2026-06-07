package handler

import (
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
	Status     string        `json:"status"`
	App        string        `json:"app"`
	Database   string        `json:"database"`
	Snapshot   snapshotEntry `json:"snapshot"`
	RestoredAt string        `json:"restoredAt"`
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

	writeJSON(w, restoreReceipt{
		Status:     "restored",
		App:        id,
		Database:   dbName,
		Snapshot:   *match,
		RestoredAt: timeNowUTC(),
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
	}
	writeJSON(w, receipt)
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
