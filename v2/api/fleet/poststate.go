package fleet

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

var poststateHex = regexp.MustCompile(`^[0-9a-f]{64}$`)
var poststateCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

func validProtectedApplyState(account, target, state string, spaces ProtectedApplyStateSpaces) bool {
	if account == "" || target == "" || (state != "present" && state != "absent") || spaces.Bucket == "" || spaces.Key == "" || spaces.Lineage == "" || spaces.Serial < 0 || spaces.VersionID == "" || !poststateHex.MatchString(spaces.RawSHA256) {
		return false
	}
	for _, value := range []string{account, target, spaces.Bucket, spaces.Key, spaces.Lineage, spaces.VersionID} {
		for _, c := range []byte(value) {
			if c < 0x21 || c > 0x7e {
				return false
			}
		}
	}
	return true
}
func (p *ProtectedApplyPrestate) Valid() bool {
	return p != nil && validProtectedApplyState(p.ProviderAccountUUID, p.ProviderTargetID, p.ProviderTargetState, p.Spaces)
}
func (r *ProtectedApplyTransitionRule) Valid() bool {
	return r != nil && (r.ProviderTargetState == "present" || r.ProviderTargetState == "absent") && (r.StateChange == "write" || r.StateChange == "no-op")
}
func (p *ObservedProtectedApplyPoststate) Valid() bool {
	return p != nil && validProtectedApplyState(p.ProviderAccountUUID, p.ProviderTargetID, p.ProviderTargetState, p.Spaces)
}

func ValidateProtectedApplyTransition(pre *ProtectedApplyPrestate, rule *ProtectedApplyTransitionRule, post *ObservedProtectedApplyPoststate) error {
	if !pre.Valid() || !rule.Valid() || !post.Valid() {
		return fmt.Errorf("protected apply state is invalid")
	}
	if pre.ProviderAccountUUID != post.ProviderAccountUUID || pre.ProviderTargetID != post.ProviderTargetID || pre.Spaces.Bucket != post.Spaces.Bucket || pre.Spaces.Key != post.Spaces.Key || pre.Spaces.Lineage != post.Spaces.Lineage || post.ProviderTargetState != rule.ProviderTargetState {
		return fmt.Errorf("protected apply poststate differs from the approved transition")
	}
	if rule.StateChange == "write" && (post.Spaces.Serial <= pre.Spaces.Serial || post.Spaces.VersionID == pre.Spaces.VersionID || post.Spaces.RawSHA256 == pre.Spaces.RawSHA256) {
		return fmt.Errorf("protected apply state write did not produce a new state object")
	}
	if rule.StateChange == "no-op" && (post.Spaces.Serial != pre.Spaces.Serial || post.Spaces.VersionID != pre.Spaces.VersionID || post.Spaces.RawSHA256 != pre.Spaces.RawSHA256) {
		return fmt.Errorf("protected apply no-op state differs from prestate")
	}
	return nil
}

func BuildProtectedApplyTerminalIntent(pre *ProtectedApplyPrestate, rule *ProtectedApplyTransitionRule, post *ObservedProtectedApplyPoststate, planID, attemptID, sourceCommit, planSHA256 string) (ProtectedApplyTerminalIntent, error) {
	if err := ValidateProtectedApplyTransition(pre, rule, post); err != nil || planID == "" || attemptID == "" || !poststateCommit.MatchString(sourceCommit) || !poststateHex.MatchString(planSHA256) {
		return ProtectedApplyTerminalIntent{}, fmt.Errorf("protected apply terminal binding is invalid")
	}
	intent := ProtectedApplyTerminalIntent{SchemaVersion: ProtectedApplyTerminalIntentSchemaVersion, PlanID: planID, AttemptID: attemptID, SourceCommit: sourceCommit, PlanSHA256: planSHA256, ApprovedPrestate: *pre, ApprovedTransitionRule: *rule, ObservedPoststate: *post}
	canonical := map[string]interface{}{"schemaVersion": intent.SchemaVersion, "planId": intent.PlanID, "attemptId": intent.AttemptID, "sourceCommit": intent.SourceCommit, "planSha256": intent.PlanSHA256, "approvedPrestate": pre, "approvedTransitionRule": rule, "observedPoststate": post}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(canonical); err != nil {
		return ProtectedApplyTerminalIntent{}, err
	}
	sum := sha256.Sum256(bytes.TrimSuffix(output.Bytes(), []byte("\n")))
	intent.CanonicalDigest = "sha256:" + hex.EncodeToString(sum[:])
	return intent, nil
}

func (i ProtectedApplyTerminalIntent) Valid() bool {
	if i.SchemaVersion != ProtectedApplyTerminalIntentSchemaVersion || ValidateProtectedApplyTransition(&i.ApprovedPrestate, &i.ApprovedTransitionRule, &i.ObservedPoststate) != nil || i.PlanID == "" || i.AttemptID == "" || !poststateCommit.MatchString(i.SourceCommit) || !poststateHex.MatchString(i.PlanSHA256) {
		return false
	}
	canonical := map[string]interface{}{"schemaVersion": i.SchemaVersion, "planId": i.PlanID, "attemptId": i.AttemptID, "sourceCommit": i.SourceCommit, "planSha256": i.PlanSHA256, "approvedPrestate": i.ApprovedPrestate, "approvedTransitionRule": i.ApprovedTransitionRule, "observedPoststate": i.ObservedPoststate}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(canonical) != nil {
		return false
	}
	sum := sha256.Sum256(bytes.TrimSuffix(output.Bytes(), []byte("\n")))
	return i.CanonicalDigest == "sha256:"+hex.EncodeToString(sum[:])
}
