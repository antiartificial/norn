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

	"norn/api/hub"
)

type snapshotInfo struct {
	Filename  string `json:"filename"`
	Database  string `json:"database"`
	CommitSha string `json:"commitSha"`
	Timestamp string `json:"timestamp"`
	SizeBytes int64  `json:"sizeBytes"`
}

func parseSnapshotFilename(name string) (database, sha, ts string, ok bool) {
	// Pattern: {db}_{sha}_{timestamp}.dump
	if !strings.HasSuffix(name, ".dump") {
		return "", "", "", false
	}
	base := strings.TrimSuffix(name, ".dump")
	parts := strings.SplitN(base, "_", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func (h *Handler) ListSnapshots(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")

	spec, err := h.loadSpec(appID)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	if spec.Services == nil || spec.Services.Postgres == nil {
		writeJSON(w, []interface{}{})
		return
	}
	dbName := spec.Services.Postgres.Database

	entries, err := os.ReadDir("snapshots")
	if err != nil {
		// No snapshots directory yet
		writeJSON(w, []interface{}{})
		return
	}

	var snapshots []snapshotInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		database, sha, tsRaw, ok := parseSnapshotFilename(entry.Name())
		if !ok || database != dbName {
			continue
		}
		t, err := time.Parse("20060102T150405", tsRaw)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		snapshots = append(snapshots, snapshotInfo{
			Filename:  entry.Name(),
			Database:  database,
			CommitSha: sha,
			Timestamp: t.UTC().Format(time.RFC3339),
			SizeBytes: info.Size(),
		})
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Timestamp > snapshots[j].Timestamp
	})

	writeJSON(w, snapshots)
}

func (h *Handler) RestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")
	ts := chi.URLParam(r, "ts")

	spec, err := h.loadSpec(appID)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	if spec.Services == nil || spec.Services.Postgres == nil {
		http.Error(w, "app has no postgres service", http.StatusBadRequest)
		return
	}
	dbName := spec.Services.Postgres.Database

	// Find matching snapshot file
	entries, err := os.ReadDir("snapshots")
	if err != nil {
		http.Error(w, "no snapshots directory", http.StatusNotFound)
		return
	}

	var match string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		database, _, tsRaw, ok := parseSnapshotFilename(entry.Name())
		if !ok || database != dbName {
			continue
		}
		t, err := time.Parse("20060102T150405", tsRaw)
		if err != nil {
			continue
		}
		if t.UTC().Format(time.RFC3339) == ts {
			match = entry.Name()
			break
		}
	}

	if match == "" {
		http.Error(w, "snapshot not found", http.StatusNotFound)
		return
	}

	filename := filepath.Join("snapshots", match)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "pg_restore", "--clean", "-d", dbName, filename)
	if out, err := cmd.CombinedOutput(); err != nil {
		http.Error(w, fmt.Sprintf("pg_restore failed: %s\n%s", err, string(out)), http.StatusInternalServerError)
		return
	}

	h.ws.Broadcast(hub.Event{
		Type:    "snapshot.restored",
		AppID:   appID,
		Payload: map[string]string{"filename": match},
	})

	writeJSON(w, map[string]string{"status": "restored", "filename": match})
}
