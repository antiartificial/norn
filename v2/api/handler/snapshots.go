package handler

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/hub"
	"norn/v2/api/model"
)

type snapshotEntry struct {
	Filename  string `json:"filename"`
	Database  string `json:"database"`
	Timestamp string `json:"timestamp"`
	Size      int64  `json:"size"`
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

	dbName := spec.Infrastructure.Postgres.Database
	entries, err := os.ReadDir("snapshots")
	if err != nil {
		writeJSON(w, []snapshotEntry{})
		return
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

		// Parse timestamp from filename: {db}_{sha}_{timestamp}.dump
		parts := strings.SplitN(strings.TrimSuffix(name, ".dump"), "_", 3)
		ts := ""
		if len(parts) == 3 {
			ts = parts[2]
		}

		snapshots = append(snapshots, snapshotEntry{
			Filename:  name,
			Database:  dbName,
			Timestamp: ts,
			Size:      info.Size(),
		})
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Timestamp > snapshots[j].Timestamp
	})

	writeJSON(w, snapshots)
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

	// Find matching snapshot file
	entries, err := os.ReadDir("snapshots")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot read snapshots directory")
		return
	}

	var matchFile string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, dbName+"_") && strings.Contains(name, ts) && strings.HasSuffix(name, ".dump") {
			matchFile = name
			break
		}
	}

	if matchFile == "" {
		writeError(w, http.StatusNotFound, fmt.Sprintf("no snapshot found for timestamp %s", ts))
		return
	}

	snapshotPath := filepath.Join("snapshots", matchFile)
	cmd := exec.CommandContext(r.Context(), "pg_restore", "--clean", "-d", dbName, snapshotPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// pg_restore returns warnings on --clean even on success
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() > 1 {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("pg_restore: %s", string(out)))
			return
		}
	}

	h.ws.Broadcast(hub.Event{
		Type:  "snapshot.restored",
		AppID: id,
		Payload: map[string]string{
			"database":  dbName,
			"snapshot":  matchFile,
			"timestamp": ts,
		},
	})

	writeJSON(w, map[string]string{
		"status":   "restored",
		"snapshot": matchFile,
		"database": dbName,
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
