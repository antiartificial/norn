package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type platformReleaseList struct {
	Current  string            `json:"current,omitempty"`
	Releases []platformRelease `json:"releases"`
}

type platformRelease struct {
	SHA            string `json:"sha"`
	Version        string `json:"version"`
	DisplayVersion string `json:"displayVersion,omitempty"`
	CreatedAt      string `json:"createdAt"`
	Path           string `json:"path"`
	Current        bool   `json:"current"`
}

var semanticReleaseVersionPattern = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)(?:-(?:control|platform))?(-[0-9]+-g[0-9a-f]{7,40})?$`)

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
		// Immutable releases are stored only under their exact source SHA. Ignore
		// atomic staging, unsigned-local rehearsal, and other maintenance
		// directories so one artifact cannot appear more than once in history.
		if !entry.IsDir() || len(entry.Name()) != 40 || !releaseSHAPrefixPattern.MatchString(entry.Name()) {
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
				if meta.Version != "" {
					release.Version = meta.Version
				}
				if meta.CreatedAt != "" {
					release.CreatedAt = meta.CreatedAt
				}
			}
		}
		out.Releases = append(out.Releases, release)
	}
	// Derive display labels from oldest to newest. A transport-tag version does
	// not carry a semantic release line, so it may inherit only the highest
	// older, timestamped semantic base. This avoids treating a later rebuild of
	// an old version as a release-line downgrade. We intentionally omit commit
	// distance rather than inventing one from directory order.
	sort.SliceStable(out.Releases, func(i, j int) bool {
		left, right := platformReleaseTime(out.Releases[i]), platformReleaseTime(out.Releases[j])
		if left.Equal(right) {
			return out.Releases[i].SHA < out.Releases[j].SHA
		}
		return left.Before(right)
	})
	nearestSemanticBase := ""
	nearestSemanticOrder := [3]int{-1, -1, -1}
	for i := range out.Releases {
		display, semanticBase := platformReleaseDisplayVersion(out.Releases[i].Version, out.Releases[i].SHA, nearestSemanticBase)
		out.Releases[i].DisplayVersion = display
		if semanticBase != "" && out.Releases[i].CreatedAt != "" {
			order := semanticVersionOrder(out.Releases[i].Version)
			if semanticVersionNewer(order, nearestSemanticOrder) {
				nearestSemanticBase = semanticBase
				nearestSemanticOrder = order
			}
		}
	}
	sort.Slice(out.Releases, func(i, j int) bool {
		left, right := platformReleaseTime(out.Releases[i]), platformReleaseTime(out.Releases[j])
		if left.Equal(right) {
			return out.Releases[i].SHA > out.Releases[j].SHA
		}
		return left.After(right)
	})
	writeJSON(w, out)
}

func platformReleaseTime(release platformRelease) time.Time {
	observed, err := time.Parse(time.RFC3339, release.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return observed
}

func platformReleaseDisplayVersion(version, sha, nearestSemanticBase string) (display, semanticBase string) {
	version = strings.TrimSpace(version)
	if match := semanticReleaseVersionPattern.FindStringSubmatch(version); match != nil {
		base := "v" + match[1] + "." + match[2] + "." + match[3]
		return base + "-platform" + match[4], base
	}
	if strings.HasPrefix(version, "v") {
		return version, ""
	}
	shortSHA := sha
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}
	if nearestSemanticBase != "" {
		return nearestSemanticBase + "-platform-g" + shortSHA, ""
	}
	return "Platform " + shortSHA, ""
}

func semanticVersionOrder(version string) [3]int {
	match := semanticReleaseVersionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if match == nil {
		return [3]int{-1, -1, -1}
	}
	var result [3]int
	for i := range result {
		result[i], _ = strconv.Atoi(match[i+1])
	}
	return result
}

func semanticVersionNewer(candidate, current [3]int) bool {
	for i := range candidate {
		if candidate[i] != current[i] {
			return candidate[i] > current[i]
		}
	}
	return false
}

func (h *Handler) PlatformRollbackRelease(w http.ResponseWriter, r *http.Request) {
	sha := strings.TrimSpace(chi.URLParam(r, "sha"))
	if sha == "" {
		writeError(w, http.StatusBadRequest, "release sha is required")
		return
	}
	if !releaseSHAPrefixPattern.MatchString(sha) {
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
