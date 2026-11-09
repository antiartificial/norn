package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const WorkloadReleaseRequestSchema = "norn.workload-release-request/v1"

// WorkloadReleaseRequestDigest identifies the caller's stable selection. It
// deliberately excludes current scheduler state, source files, and encrypted
// input bytes so an exact retry can replay before reading those mutable facts.
func WorkloadReleaseRequestDigest(qualificationID string, targetReplicas int) (string, error) {
	if strings.TrimSpace(qualificationID) == "" || qualificationID != strings.TrimSpace(qualificationID) || targetReplicas < 1 {
		return "", fmt.Errorf("workload release request selection is incomplete")
	}
	canonical, err := json.Marshal(struct {
		SchemaVersion   string `json:"schemaVersion"`
		QualificationID string `json:"qualificationId"`
		TargetReplicas  int    `json:"targetReplicas"`
	}{WorkloadReleaseRequestSchema, qualificationID, targetReplicas})
	if err != nil {
		return "", fmt.Errorf("encode workload release request selection: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
