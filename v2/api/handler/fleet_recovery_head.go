package handler

import (
	"regexp"
	"strconv"
	"strings"
)

var recoveryHeadSHARe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// recoveryWorkflowHeadSHA keeps the reviewed plan commit distinct from the
// GitHub commit that executed a recovery. New attempts record the latter from
// their authenticated Actions OIDC identity. An older attempt has no such
// field: only an exact operator-provided incident binding can fill it in.
// The binding is planID:runID:runAttempt:planCommit:workflowHead.
func recoveryWorkflowHeadSHA(planID, runnerAttemptID, planCommit, recordedHead, legacyBinding string) string {
	if recordedHead != "" {
		if recoveryHeadSHARe.MatchString(recordedHead) {
			return recordedHead
		}
		return ""
	}
	parts := strings.Split(legacyBinding, ":")
	if len(parts) != 5 || parts[0] != planID || parts[3] != planCommit || !recoveryHeadSHARe.MatchString(parts[3]) || !recoveryHeadSHARe.MatchString(parts[4]) {
		return ""
	}
	_, runID, runAttempt, ok := parseFleetRunnerAttemptID(runnerAttemptID)
	if !ok {
		return ""
	}
	boundRunID, errRun := strconv.ParseInt(parts[1], 10, 64)
	boundAttempt, errAttempt := strconv.ParseInt(parts[2], 10, 64)
	if errRun != nil || errAttempt != nil || boundRunID != runID || boundAttempt != runAttempt {
		return ""
	}
	return parts[4]
}
