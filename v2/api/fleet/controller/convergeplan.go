package controller

// Converge-plan builder (plan-5-7 section 2.6, work package E4a). A converge
// plan is an ordinary fleet.capacity-plan operation payload with Action
// "converge" and one additive omitempty object "converge". The builder and the
// recorder interface are backend neutral: the PG recorder lives in
// handler/fleet_converge.go (storage packages import this one, so it cannot
// import them) and the etcd recorder is E6's.
//
// fleet.CapacityPlan itself is not modified. ConvergeCapacityPlan embeds it and
// adds the converge object last, so a plan without it marshals byte-identically
// to the plain CapacityPlan and keeps its digest (ConvergePlanFingerprintsUnchanged).

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/executor"
)

const (
	convergePlanSchemaVersion = "norn.fleet-capacity-plan/v1"
	// ConvergeStrategy is the Strategy field of a converge plan; the executor
	// lane has no replacement strategy of its own.
	ConvergeStrategy = "controller"
)

// ConvergeCapacityPlan is a capacity plan with the optional converge object.
type ConvergeCapacityPlan struct {
	fleet.CapacityPlan
	Converge *executor.ConvergePlan `json:"converge,omitempty"`
}

// ConvergePlanInput is everything the builder needs. SigningKey may be empty
// (development): the plan is then unsigned and carries the same warning the
// scale/replace plans carry.
type ConvergePlanInput struct {
	Resource    string
	Cluster     string
	Repository  string
	CommitSHA   string
	LastApplied string // base commit; "" on the first converge
	// PathClass is executor.PathClassConfig or PathClassExecutable. Inert has
	// no plan.
	PathClass   string
	MergeActor  string
	PullRequest int
	// Generation is the desired generation this plan records.
	Generation int64
	SigningKey string
	// NewID defaults to uuid.NewString.
	NewID func() string
}

// BuildConvergePlan builds, digests and signs a converge plan. Executable
// ranges and the first converge (no LastApplied) always require approval;
// other config ranges auto-apply (D2). The first-converge rule is set here,
// on the stored flag admission reads, and not only inferred by the watcher's
// path class, so admission and controller.ConvergeNeedsApproval agree for
// every caller.
func BuildConvergePlan(in ConvergePlanInput) (*ConvergeCapacityPlan, error) {
	if in.Cluster == "" {
		return nil, fmt.Errorf("converge plan requires a cluster")
	}
	converge := executor.ConvergePlan{
		Resource: in.Resource, Repository: in.Repository, CommitSHA: in.CommitSHA, BaseCommitSHA: in.LastApplied,
		PathClass: in.PathClass, RequiresApproval: in.PathClass == executor.PathClassExecutable || in.LastApplied == "",
		MergeActor: in.MergeActor, PullRequest: in.PullRequest, Generation: in.Generation,
	}
	if err := converge.Validate(); err != nil {
		return nil, err
	}
	newID := in.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	sourceDigest := sha256.Sum256([]byte("fleet-converge:" + in.Repository + "@" + in.CommitSHA))
	findings := []fleet.Finding{{Severity: "info", Code: "fleet.plan.converge", Field: "converge", Message: "converge " + in.Resource + " to " + in.CommitSHA + " (" + in.PathClass + ")"}}
	if converge.RequiresApproval {
		findings = append(findings, fleet.Finding{Severity: "info", Code: "fleet.plan.converge.requires-approval", Field: "converge.requiresApproval", Message: "executable or first-time change; a one-click approve is required before the executor starts", Remediation: "POST /api/v1/fleet/plans/{planID}/converge/approve"})
	}
	if in.SigningKey == "" {
		findings = append(findings, fleet.Finding{Severity: "warning", Code: "fleet.plan.unsigned-development", Field: "signature", Message: "plan is durable but unsigned because NORN_AUDIT_SIGNING_KEY is not configured", Remediation: "Production mode requires an audit signing key."})
	}
	plan := &ConvergeCapacityPlan{
		CapacityPlan: fleet.CapacityPlan{
			SchemaVersion: convergePlanSchemaVersion, ID: newID(), Cluster: in.Cluster, Pool: in.Resource,
			Action: executor.ActionConverge, Strategy: ConvergeStrategy, Reason: "converge to protected main " + in.CommitSHA,
			SourceDigest: "sha256:" + hex.EncodeToString(sourceDigest[:]), Findings: findings,
		},
		Converge: &converge,
	}
	sort.SliceStable(plan.Findings, func(i, j int) bool { return plan.Findings[i].Code < plan.Findings[j].Code })
	canonical, err := CanonicalConvergePlan(plan)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	plan.Digest = "sha256:" + hex.EncodeToString(sum[:])
	if in.SigningKey != "" {
		plan.Signature = convergeSignature(in.SigningKey, plan.Digest)
	}
	return plan, nil
}

// CanonicalConvergePlan is the plan's canonical bytes: digest and signature
// cleared, findings sorted. For a plan without converge they equal the
// handler's canonicalCapacityPlan bytes exactly.
func CanonicalConvergePlan(plan *ConvergeCapacityPlan) ([]byte, error) {
	canonical := *plan
	canonical.Digest = ""
	canonical.Signature = ""
	canonical.Findings = append([]fleet.Finding(nil), plan.Findings...)
	sort.SliceStable(canonical.Findings, func(i, j int) bool { return canonical.Findings[i].Code < canonical.Findings[j].Code })
	return json.Marshal(canonical)
}

func convergeSignature(key, digest string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(digest))
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyConvergePlan checks the digest and, when keys are given, the HMAC
// signature against any non-empty key (current and previous). It is the
// converge-aware counterpart of the handler's verifyCapacityPlan, which decodes
// into a plain CapacityPlan and therefore rejects converge plans (fail closed
// for the GitHub lane).
func VerifyConvergePlan(plan *ConvergeCapacityPlan, keys []string) bool {
	if plan == nil || plan.Converge == nil || plan.Action != executor.ActionConverge || plan.Converge.Validate() != nil {
		return false
	}
	canonical, err := CanonicalConvergePlan(plan)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(canonical)
	if plan.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		return false
	}
	for _, key := range keys {
		if key != "" && hmac.Equal([]byte(plan.Signature), []byte(convergeSignature(key, plan.Digest))) {
			return true
		}
	}
	return false
}

// PlanSHA256 is the hex part of the digest: the attempt-start planSha256.
func (p *ConvergeCapacityPlan) PlanSHA256() string { return strings.TrimPrefix(p.Digest, "sha256:") }

// RecordedConverge is a converge plan as stored by a recorder.
type RecordedConverge struct {
	PlanID   string
	Digest   string
	Converge executor.ConvergePlan
	// Admissible is false when the newest plan was refused at admission with
	// fleet_plan_revalidation_required; only then may a rescan record a new
	// plan for the same commit. Only Rescan sees this for a settled resource:
	// a poll that decided "exists" skips the resource until the tip moves.
	Admissible bool
}

// ConvergePlanRecorder is the backend seam (PG: handler/fleet_converge.go,
// etcd: E6). Implementations record through the existing signed
// fleet.capacity-plan operation path.
type ConvergePlanRecorder interface {
	// NewestConvergePlan returns the newest converge plan recorded for the
	// resource, or nil when there is none.
	NewestConvergePlan(ctx context.Context, resource string) (*RecordedConverge, error)
	// RecordConvergePlan durably records plan. key is an idempotency key: a
	// repeat with the same key returns the first plan instead of a second one.
	RecordConvergePlan(ctx context.Context, plan *ConvergeCapacityPlan, key string) (*RecordedConverge, error)
}
