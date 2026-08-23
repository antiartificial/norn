package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type platformReleaseList struct {
	Current  string            `json:"current,omitempty"`
	Releases []platformRelease `json:"releases"`
}

type platformRelease struct {
	SHA       string `json:"sha"`
	Version   string `json:"version"`
	CreatedAt string `json:"createdAt"`
	Path      string `json:"path"`
	Current   bool   `json:"current"`
}

func (h *Handler) PlatformReleases(w http.ResponseWriter, r *http.Request) {
	releasesDir := firstEnv("NORN_RELEASES_DIR", filepath.Join(homeDir(), "norn", "releases"))
	currentLink := firstEnv("NORN_CURRENT_LINK", filepath.Join(homeDir(), "norn", "current"))
	current, _ := os.Readlink(currentLink)
	out := platformReleaseList{Current: current, Releases: []platformRelease{}}

	entries, err := os.ReadDir(releasesDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, out)
			return
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "release_list_failed", "failed to list platform releases")
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		releasePath := filepath.Join(releasesDir, entry.Name())
		release := platformRelease{
			SHA:     entry.Name(),
			Version: entry.Name(),
			Path:    releasePath,
			Current: samePath(current, releasePath),
		}
		if info, err := entry.Info(); err == nil {
			release.CreatedAt = info.ModTime().UTC().Format(time.RFC3339)
		}
		data, err := os.ReadFile(filepath.Join(releasePath, "release.json"))
		if err == nil {
			var meta struct {
				SHA       string `json:"sha"`
				Version   string `json:"version"`
				CreatedAt string `json:"createdAt"`
				Path      string `json:"path"`
			}
			if json.Unmarshal(data, &meta) == nil {
				if meta.SHA != "" {
					release.SHA = meta.SHA
				}
				if meta.Version != "" {
					release.Version = meta.Version
				}
				if meta.CreatedAt != "" {
					release.CreatedAt = meta.CreatedAt
				}
				if meta.Path != "" {
					release.Path = meta.Path
				}
			}
		}
		out.Releases = append(out.Releases, release)
	}
	sort.Slice(out.Releases, func(i, j int) bool {
		return out.Releases[i].CreatedAt > out.Releases[j].CreatedAt
	})
	writeJSON(w, out)
}

func (h *Handler) PlatformRollbackRelease(w http.ResponseWriter, r *http.Request) {
	sha := strings.TrimSpace(chi.URLParam(r, "sha"))
	if sha == "" {
		writeError(w, http.StatusBadRequest, "release sha is required")
		return
	}
	if !maintenanceRefPattern.MatchString(sha) {
		writeError(w, http.StatusBadRequest, "invalid release sha")
		return
	}
	h.queueMaintenanceOperation(w, r, "platform.rollback", sha, "control-plane rollback", map[string]interface{}{"sha": sha})
}

func firstEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "."
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return aa == bb
	}
	return a == b
}
