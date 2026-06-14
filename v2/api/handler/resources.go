package handler

import (
	"net/http"
	"sort"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

type resourceSuggestion struct {
	App            string `json:"app"`
	Process        string `json:"process"`
	DeclaredMemMB  int    `json:"declaredMemoryMB"`
	DeclaredCPUMHz int    `json:"declaredCpuMHz"`
	UsedMemMB      int    `json:"usedMemoryMB"`
	PeakMemMB      int    `json:"peakMemoryMB"`
	CPUPercent     float64 `json:"cpuPercent"`
	Status         string `json:"status"`
	Reason         string `json:"reason"`
}

func classifyMemory(declaredMB, usedMB, peakMB int) (string, string) {
	highMB := peakMB
	if highMB == 0 {
		highMB = usedMB
	}
	if declaredMB == 0 || highMB == 0 {
		return "unknown", "no usage data"
	}
	ratio := float64(highMB) / float64(declaredMB)
	if ratio > 0.80 {
		return "at_risk", "memory exceeds 80% of limit"
	}
	if ratio < 0.30 {
		return "overprovisioned", "memory below 30% of limit"
	}
	return "right_sized", ""
}

func (h *Handler) ResourceSuggestions(w http.ResponseWriter, r *http.Request) {
	if h.nomad == nil {
		writeError(w, http.StatusServiceUnavailable, "nomad not connected")
		return
	}

	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].App < specs[j].App })

	var suggestions []resourceSuggestion
	for _, spec := range specs {
		usageByGroup := map[string]*nomad.ResourceUsage{}
		usage, err := h.nomad.JobResourceUsage(spec.App)
		if err != nil || len(usage) == 0 {
			continue
		}
		for i := range usage {
			u := &usage[i]
			if existing, ok := usageByGroup[u.TaskGroup]; ok {
				if u.MemoryUsageBytes > existing.MemoryUsageBytes {
					existing.MemoryUsageBytes = u.MemoryUsageBytes
				}
				if u.MemoryMaxBytes > existing.MemoryMaxBytes {
					existing.MemoryMaxBytes = u.MemoryMaxBytes
				}
				if u.CPUPercent > existing.CPUPercent {
					existing.CPUPercent = u.CPUPercent
				}
			} else {
				usageByGroup[u.TaskGroup] = u
			}
		}

		for procName, proc := range spec.Processes {
			u, ok := usageByGroup[procName]
			if !ok {
				continue
			}
			declaredMem := 128
			declaredCPU := 100
			if proc.Resources != nil {
				if proc.Resources.Memory > 0 {
					declaredMem = proc.Resources.Memory
				}
				if proc.Resources.CPU > 0 {
					declaredCPU = proc.Resources.CPU
				}
			}

			usedMB := int(u.MemoryUsageBytes / (1024 * 1024))
			peakMB := int(u.MemoryMaxBytes / (1024 * 1024))
			status, reason := classifyMemory(declaredMem, usedMB, peakMB)

			suggestions = append(suggestions, resourceSuggestion{
				App:            spec.App,
				Process:        procName,
				DeclaredMemMB:  declaredMem,
				DeclaredCPUMHz: declaredCPU,
				UsedMemMB:      usedMB,
				PeakMemMB:      peakMB,
				CPUPercent:     u.CPUPercent,
				Status:         status,
				Reason:         reason,
			})
		}
	}

	writeJSON(w, map[string]interface{}{
		"suggestions": suggestions,
	})
}
