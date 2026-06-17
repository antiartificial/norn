package engine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var (
	knownAppsMu sync.RWMutex
	knownApps   []string // sorted longest-first for prefix matching
)

// SetKnownApps registers app names so ParseContainerName can unambiguously
// split hyphenated "app-process" strings. Call this on startup after
// discovering apps from infraspec.
func SetKnownApps(apps []string) {
	sorted := make([]string, len(apps))
	copy(sorted, apps)
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i]) > len(sorted[j])
	})
	knownAppsMu.Lock()
	knownApps = sorted
	knownAppsMu.Unlock()
}

const containerPrefix = "norn"

func ContainerName(app, process string, replica int) string {
	return fmt.Sprintf("%s-%s-%s-%d", containerPrefix, app, process, replica)
}

func CanaryName(app, process string, replica int) string {
	return fmt.Sprintf("%s-%s-%s-canary-%d", containerPrefix, app, process, replica)
}

func CronRunName(app, process string, ts int64) string {
	return fmt.Sprintf("%s-%s-%s-cron-%d", containerPrefix, app, process, ts)
}

func BatchName(app, process string, ts int64) string {
	return fmt.Sprintf("%s-%s-%s-fn-%d", containerPrefix, app, process, ts)
}

type ParsedName struct {
	App     string
	Process string
	Replica int
	Kind    string // "service", "canary", "cron", "batch"
	Ts      int64  // unix timestamp for cron/batch
}

func ParseContainerName(name string) (ParsedName, error) {
	if !IsNornContainer(name) {
		return ParsedName{}, fmt.Errorf("not a norn container: %s", name)
	}

	// Strip "norn-" prefix
	rest := name[len(containerPrefix)+1:]
	parts := strings.Split(rest, "-")
	if len(parts) < 3 {
		return ParsedName{}, fmt.Errorf("invalid container name: %s", name)
	}

	// Identify the kind by scanning from the end for known markers.
	// Names can be: app-proc-N, app-proc-canary-N, app-proc-cron-TS, app-proc-fn-TS
	// Both app and process can contain hyphens, so we search for markers from the right.

	last := parts[len(parts)-1]

	// Check for canary: ...-canary-N
	if len(parts) >= 4 && parts[len(parts)-2] == "canary" {
		replica, err := strconv.Atoi(last)
		if err != nil {
			return ParsedName{}, fmt.Errorf("invalid canary replica in %s: %w", name, err)
		}
		appProcess := strings.Join(parts[:len(parts)-2], "-")
		app, process, err := splitAppProcess(appProcess)
		if err != nil {
			return ParsedName{}, fmt.Errorf("invalid canary name %s: %w", name, err)
		}
		return ParsedName{App: app, Process: process, Replica: replica, Kind: "canary"}, nil
	}

	// Check for cron: ...-cron-TS
	if len(parts) >= 4 && parts[len(parts)-2] == "cron" {
		ts, err := strconv.ParseInt(last, 10, 64)
		if err != nil {
			return ParsedName{}, fmt.Errorf("invalid cron timestamp in %s: %w", name, err)
		}
		appProcess := strings.Join(parts[:len(parts)-2], "-")
		app, process, err := splitAppProcess(appProcess)
		if err != nil {
			return ParsedName{}, fmt.Errorf("invalid cron name %s: %w", name, err)
		}
		return ParsedName{App: app, Process: process, Kind: "cron", Ts: ts}, nil
	}

	// Check for batch/function: ...-fn-TS
	if len(parts) >= 4 && parts[len(parts)-2] == "fn" {
		ts, err := strconv.ParseInt(last, 10, 64)
		if err != nil {
			return ParsedName{}, fmt.Errorf("invalid batch timestamp in %s: %w", name, err)
		}
		appProcess := strings.Join(parts[:len(parts)-2], "-")
		app, process, err := splitAppProcess(appProcess)
		if err != nil {
			return ParsedName{}, fmt.Errorf("invalid batch name %s: %w", name, err)
		}
		return ParsedName{App: app, Process: process, Kind: "batch", Ts: ts}, nil
	}

	// Default: service container ...-N
	replica, err := strconv.Atoi(last)
	if err != nil {
		return ParsedName{}, fmt.Errorf("invalid service replica in %s: %w", name, err)
	}
	appProcess := strings.Join(parts[:len(parts)-1], "-")
	app, process, err := splitAppProcess(appProcess)
	if err != nil {
		return ParsedName{}, fmt.Errorf("invalid service name %s: %w", name, err)
	}
	return ParsedName{App: app, Process: process, Replica: replica, Kind: "service"}, nil
}

// splitAppProcess splits a combined "app-process" string. Since both app and
// process names can contain hyphens, we need context to split correctly.
// Convention: app names match [a-z0-9][a-z0-9-]* and process names are
// typically short (web, worker, api, cron-backup). We split on the first
// hyphen that produces a valid pair, preferring the longest app prefix that
// leaves a non-empty process.
//
// For unambiguous parsing, callers should register known app names via
// SetKnownApps. Without that context, we fall back to splitting at the
// first hyphen.
func splitAppProcess(combined string) (string, string, error) {
	if combined == "" {
		return "", "", fmt.Errorf("empty app-process string")
	}

	knownAppsMu.RLock()
	apps := knownApps
	knownAppsMu.RUnlock()

	if len(apps) > 0 {
		// Try longest matching known app first
		for _, app := range apps {
			if strings.HasPrefix(combined, app+"-") {
				process := combined[len(app)+1:]
				if process != "" {
					return app, process, nil
				}
			}
		}
	}

	// Fallback: split at first hyphen
	idx := strings.Index(combined, "-")
	if idx <= 0 || idx >= len(combined)-1 {
		return "", "", fmt.Errorf("cannot split app-process: %s", combined)
	}
	return combined[:idx], combined[idx+1:], nil
}

func IsNornContainer(name string) bool {
	return strings.HasPrefix(name, containerPrefix+"-")
}

// ShortID returns the first 8 characters of a container name for display,
// matching the allocation short-ID convention.
func ShortID(containerName string) string {
	if len(containerName) <= 8 {
		return containerName
	}
	return containerName[:8]
}
