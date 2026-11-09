// Package savedplaneffect defines Norn's narrow, durable authorization record
// for one supervised OpenTofu apply of the already reviewed app-04 retirement
// plan. It never carries provider credentials and cannot authorize raw HTTP
// deletion.
package savedplaneffect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"sort"
	"time"

	"github.com/google/uuid"
)

const (
	ClaimSchema           = "norn.fleet-saved-plan-effect-claim/v1"
	PlanBindingSchema     = "norn.fleet-saved-plan-effect-plan-binding/v1"
	DispatchBindingSchema = "norn.fleet-saved-plan-effect-dispatch-binding/v1"
	GrantSchema           = "norn.fleet-saved-plan-effect-grant/v1"
	RefreshOnlySchema     = "norn.fleet-saved-plan-refresh-only/v1"
	App03                 = "app-03"
	App04                 = "app-04"
	App03DropletAddress   = `module.cluster.digitalocean_droplet.node["app-03"]`
	App04DropletAddress   = `module.cluster.digitalocean_droplet.node["app-04"]`
	StagingAppContraction = "staging-app-contraction"
	ApplyCommand          = "tofu apply -input=false <saved-binary-plan>"
	RefreshOnlyCommand    = "tofu plan -refresh-only -input=false"
	StagingBackendKey     = "norn-work-fleet-state/staging/nyc3/terraform.tfstate"
	MaxClaimLifetime      = 15 * time.Minute
	MaxApplyGrantLifetime = time.Minute
)

var (
	hex40       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	decimal     = regexp.MustCompile(`^[1-9][0-9]*$`)
	ErrInvalid  = errors.New("saved plan effect contract is invalid")
	ErrConsumed = errors.New("saved plan effect apply authority was already consumed")
)

// DeleteAction is one reviewed OpenTofu delete. Every element must refer to
// the exact app-pool provider identity selected by its Claim; updates,
// replacements and unrelated nodes are deliberately not representable.
type DeleteAction struct {
	Address    string `json:"address"`
	Action     string `json:"action"`
	DropletID  string `json:"dropletId"`
	DropletURN string `json:"dropletUrn"`
}

// Claim is submitted by the independently credentialed verifier before the
// executor can ask for the single apply envelope. Norn checks its own plan,
// attempt and fence facts separately; verifier-owned provider, state and
// drain facts remain digests rather than untrusted success booleans.
type Claim struct {
	SchemaVersion               string         `json:"schemaVersion"`
	ID                          string         `json:"id"`
	PlanID                      string         `json:"planId"`
	PlanSHA256                  string         `json:"planSha256"`
	AttemptID                   string         `json:"attemptId"`
	ProtectedCommit             string         `json:"protectedCommit"`
	GitHubRunID                 int64          `json:"githubRunId"`
	GitHubRunAttempt            int            `json:"githubRunAttempt"`
	GitHubJobID                 int64          `json:"githubJobId"`
	GitHubJobName               string         `json:"githubJobName"`
	GitHubWorkflowRef           string         `json:"githubWorkflowRef"`
	GitHubWorkflowSHA           string         `json:"githubWorkflowSha"`
	PlanRunID                   int64          `json:"planRunId"`
	PlanArtifactSHA256          string         `json:"planArtifactSha256"`
	DispatchNonceSHA256         string         `json:"dispatchNonceSha256"`
	SavedBinaryPlanSHA256       string         `json:"savedBinaryPlanSha256"`
	SavedJSONPlanSHA256         string         `json:"savedJsonPlanSha256"`
	PreparationProjectionSHA256 string         `json:"preparationProjectionSha256"`
	PreparedPlanReceiptSHA256   string         `json:"preparedPlanReceiptSha256"`
	BackendKey                  string         `json:"backendKey"`
	StateLineage                string         `json:"stateLineage"`
	PreSerial                   uint64         `json:"preSerial"`
	WriterGeneration            uint64         `json:"writerGeneration"`
	WriterHolder                string         `json:"writerHolder"`
	TargetID                    string         `json:"targetId"`
	TargetFenceGeneration       int64          `json:"targetFenceGeneration"`
	AuthorityEpoch              int64          `json:"authorityEpoch"`
	Node                        string         `json:"node"`
	DropletID                   string         `json:"dropletId"`
	DropletURN                  string         `json:"dropletUrn"`
	DeleteActions               []DeleteAction `json:"deleteActions"`
	TransitionKind              string         `json:"transitionKind"`
	FromDesired                 int            `json:"fromDesired"`
	ToDesired                   int            `json:"toDesired"`
	DrainReceiptSHA256          string         `json:"drainReceiptSha256"`
	VerifierAuthorizationSHA256 string         `json:"verifierAuthorizationSha256"`
	CreatedAt                   time.Time      `json:"createdAt"`
	ExpiresAt                   time.Time      `json:"expiresAt"`
}

// PlanBinding is the immutable, pre-run portion of a saved-plan effect. The
// verifier creates it through Norn's plan-level, create-only binding route
// after the capacity plan itself is accepted and before a GitHub job is
// assigned. It is not embedded in the capacity-plan payload: run and job
// identity do not exist at that point. The post-drain Claim adds attempt,
// fence, writer, receipt and expiry facts; it may not redefine any binding
// field below.
type PlanBinding struct {
	SchemaVersion               string         `json:"schemaVersion"`
	PlanID                      string         `json:"planId"`
	PlanSHA256                  string         `json:"planSha256"`
	ProtectedCommit             string         `json:"protectedCommit"`
	SavedBinaryPlanSHA256       string         `json:"savedBinaryPlanSha256"`
	SavedJSONPlanSHA256         string         `json:"savedJsonPlanSha256"`
	PreparationProjectionSHA256 string         `json:"preparationProjectionSha256"`
	PreparedPlanReceiptSHA256   string         `json:"preparedPlanReceiptSha256"`
	BackendKey                  string         `json:"backendKey"`
	StateLineage                string         `json:"stateLineage"`
	PreSerial                   uint64         `json:"preSerial"`
	Node                        string         `json:"node"`
	DropletID                   string         `json:"dropletId"`
	DropletURN                  string         `json:"dropletUrn"`
	DeleteActions               []DeleteAction `json:"deleteActions"`
	TransitionKind              string         `json:"transitionKind"`
	FromDesired                 int            `json:"fromDesired"`
	ToDesired                   int            `json:"toDesired"`
}

func planBindingFromClaim(c Claim) PlanBinding {
	return PlanBinding{SchemaVersion: PlanBindingSchema, PlanID: c.PlanID, PlanSHA256: c.PlanSHA256, ProtectedCommit: c.ProtectedCommit,
		SavedBinaryPlanSHA256: c.SavedBinaryPlanSHA256, SavedJSONPlanSHA256: c.SavedJSONPlanSHA256, PreparationProjectionSHA256: c.PreparationProjectionSHA256, PreparedPlanReceiptSHA256: c.PreparedPlanReceiptSHA256, BackendKey: c.BackendKey, StateLineage: c.StateLineage, PreSerial: c.PreSerial,
		Node: c.Node, DropletID: c.DropletID, DropletURN: c.DropletURN, DeleteActions: append([]DeleteAction(nil), c.DeleteActions...), TransitionKind: c.TransitionKind, FromDesired: c.FromDesired, ToDesired: c.ToDesired}
}

// PlanBindingForClaim derives the immutable portion that must already be
// durably recorded beside the accepted Norn capacity plan. It is useful to
// controllers constructing a test fixture; production callers must create the
// binding before the post-drain Claim exists.
func PlanBindingForClaim(c Claim) PlanBinding { return planBindingFromClaim(c) }

func (p PlanBinding) Validate() error {
	if p.SchemaVersion != PlanBindingSchema {
		return ErrInvalid
	}
	// Reuse the Claim's exact identity/action validation with inert, valid
	// post-run fields. PlanBinding must not carry those mutable facts itself.
	probe := Claim{SchemaVersion: ClaimSchema, ID: uuid.NewString(), PlanID: p.PlanID, PlanSHA256: p.PlanSHA256, AttemptID: uuid.NewString(), ProtectedCommit: p.ProtectedCommit,
		GitHubRunID: 1, GitHubRunAttempt: 1, GitHubJobID: 1, GitHubJobName: "post-dispatch", GitHubWorkflowRef: "post-dispatch@0000000000000000000000000000000000000000", GitHubWorkflowSHA: "0000000000000000000000000000000000000000", PlanRunID: 1, PlanArtifactSHA256: "0000000000000000000000000000000000000000000000000000000000000000", DispatchNonceSHA256: "0000000000000000000000000000000000000000000000000000000000000000", SavedBinaryPlanSHA256: p.SavedBinaryPlanSHA256, SavedJSONPlanSHA256: p.SavedJSONPlanSHA256, PreparationProjectionSHA256: p.PreparationProjectionSHA256, PreparedPlanReceiptSHA256: p.PreparedPlanReceiptSHA256, BackendKey: p.BackendKey, StateLineage: p.StateLineage, PreSerial: p.PreSerial,
		WriterGeneration: 1, TargetID: "plan-binding", TargetFenceGeneration: 1, AuthorityEpoch: 1, Node: p.Node, DropletID: p.DropletID, DropletURN: p.DropletURN, DeleteActions: p.DeleteActions, TransitionKind: p.TransitionKind, FromDesired: p.FromDesired, ToDesired: p.ToDesired,
		DrainReceiptSHA256: "0000000000000000000000000000000000000000000000000000000000000000", VerifierAuthorizationSHA256: "0000000000000000000000000000000000000000000000000000000000000000", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute)}
	probe.WriterHolder = uuid.NewString()
	if p.BackendKey != StagingBackendKey {
		return ErrInvalid
	}
	return probe.Validate()
}

// MatchesPlanBinding holds post-run claims to the exact immutable pre-run
// facts accepted in Norn's capacity plan.
func (c Claim) MatchesPlanBinding(p PlanBinding) bool {
	return p.Validate() == nil && c.Validate() == nil && reflect.DeepEqual(planBindingFromClaim(c), p)
}

// DispatchBinding is durably recorded after protected dispatch has allocated
// the exact GitHub run/job. It cannot be embedded in the original capacity
// plan because that plan predates GitHub's job identifiers.
type DispatchBinding struct {
	SchemaVersion       string `json:"schemaVersion"`
	PlanID              string `json:"planId"`
	PlanSHA256          string `json:"planSha256"`
	ProtectedCommit     string `json:"protectedCommit"`
	GitHubRunID         int64  `json:"githubRunId"`
	GitHubRunAttempt    int    `json:"githubRunAttempt"`
	GitHubJobID         int64  `json:"githubJobId"`
	GitHubJobName       string `json:"githubJobName"`
	GitHubWorkflowRef   string `json:"githubWorkflowRef"`
	GitHubWorkflowSHA   string `json:"githubWorkflowSha"`
	PlanRunID           int64  `json:"planRunId"`
	PlanArtifactSHA256  string `json:"planArtifactSha256"`
	DispatchNonceSHA256 string `json:"dispatchNonceSha256"`
}

func dispatchBindingFromClaim(c Claim) DispatchBinding {
	return DispatchBinding{SchemaVersion: DispatchBindingSchema, PlanID: c.PlanID, PlanSHA256: c.PlanSHA256, ProtectedCommit: c.ProtectedCommit, GitHubRunID: c.GitHubRunID, GitHubRunAttempt: c.GitHubRunAttempt, GitHubJobID: c.GitHubJobID, GitHubJobName: c.GitHubJobName, GitHubWorkflowRef: c.GitHubWorkflowRef, GitHubWorkflowSHA: c.GitHubWorkflowSHA, PlanRunID: c.PlanRunID, PlanArtifactSHA256: c.PlanArtifactSHA256, DispatchNonceSHA256: c.DispatchNonceSHA256}
}

func DispatchBindingForClaim(c Claim) DispatchBinding { return dispatchBindingFromClaim(c) }

func (p DispatchBinding) Validate() error {
	probe := Claim{SchemaVersion: ClaimSchema, ID: uuid.NewString(), PlanID: p.PlanID, PlanSHA256: p.PlanSHA256, AttemptID: uuid.NewString(), ProtectedCommit: p.ProtectedCommit,
		GitHubRunID: p.GitHubRunID, GitHubRunAttempt: p.GitHubRunAttempt, GitHubJobID: p.GitHubJobID, GitHubJobName: p.GitHubJobName, GitHubWorkflowRef: p.GitHubWorkflowRef, GitHubWorkflowSHA: p.GitHubWorkflowSHA, PlanRunID: p.PlanRunID, PlanArtifactSHA256: p.PlanArtifactSHA256, DispatchNonceSHA256: p.DispatchNonceSHA256,
		SavedBinaryPlanSHA256: "0000000000000000000000000000000000000000000000000000000000000000", SavedJSONPlanSHA256: "0000000000000000000000000000000000000000000000000000000000000000", PreparationProjectionSHA256: "0000000000000000000000000000000000000000000000000000000000000000", PreparedPlanReceiptSHA256: "0000000000000000000000000000000000000000000000000000000000000000", BackendKey: StagingBackendKey, StateLineage: "binding", PreSerial: 1, WriterGeneration: 1, WriterHolder: uuid.NewString(), TargetID: "binding", TargetFenceGeneration: 1, AuthorityEpoch: 1, Node: App03, DropletID: "1", DropletURN: "do:droplet:1", DeleteActions: []DeleteAction{{Address: App03DropletAddress, Action: "delete", DropletID: "1", DropletURN: "do:droplet:1"}}, TransitionKind: StagingAppContraction, FromDesired: 3, ToDesired: 2,
		DrainReceiptSHA256: "0000000000000000000000000000000000000000000000000000000000000000", VerifierAuthorizationSHA256: "0000000000000000000000000000000000000000000000000000000000000000", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute)}
	if p.SchemaVersion != DispatchBindingSchema {
		return ErrInvalid
	}
	return probe.Validate()
}

func (c Claim) MatchesDispatchBinding(p DispatchBinding) bool {
	return p.Validate() == nil && c.Validate() == nil && reflect.DeepEqual(dispatchBindingFromClaim(c), p)
}

func (c Claim) Validate() error {
	if c.SchemaVersion != ClaimSchema || uuid.Validate(c.ID) != nil || uuid.Validate(c.PlanID) != nil || uuid.Validate(c.AttemptID) != nil ||
		!hex64.MatchString(c.PlanSHA256) || !hex40.MatchString(c.ProtectedCommit) || c.GitHubRunID <= 0 || c.GitHubRunAttempt <= 0 || c.GitHubJobID <= 0 || c.GitHubJobName == "" || c.GitHubWorkflowRef == "" || !hex40.MatchString(c.GitHubWorkflowSHA) || c.PlanRunID <= 0 || !hex64.MatchString(c.PlanArtifactSHA256) || !hex64.MatchString(c.DispatchNonceSHA256) || !hex64.MatchString(c.SavedBinaryPlanSHA256) ||
		!hex64.MatchString(c.SavedJSONPlanSHA256) || !hex64.MatchString(c.PreparationProjectionSHA256) || !hex64.MatchString(c.PreparedPlanReceiptSHA256) || c.BackendKey != StagingBackendKey || c.StateLineage == "" || c.PreSerial == 0 ||
		c.WriterGeneration == 0 || uuid.Validate(c.WriterHolder) != nil || c.TargetID == "" || c.TargetFenceGeneration <= 0 || c.AuthorityEpoch <= 0 ||
		c.TransitionKind != StagingAppContraction || !validContractionTarget(c.Node, c.FromDesired, c.ToDesired) || !decimal.MatchString(c.DropletID) || c.DropletURN != "do:droplet:"+c.DropletID ||
		!hex64.MatchString(c.DrainReceiptSHA256) || !hex64.MatchString(c.VerifierAuthorizationSHA256) ||
		c.CreatedAt.IsZero() || c.ExpiresAt.IsZero() || !c.ExpiresAt.After(c.CreatedAt) || c.ExpiresAt.Sub(c.CreatedAt) > MaxClaimLifetime ||
		!validDeleteActions(c.Node, c.DeleteActions, c.DropletID, c.DropletURN) {
		return ErrInvalid
	}
	return nil
}

// validContractionTarget admits exactly the two explicitly ordered app-pool
// reductions. The M4 qualification uses app-03 (3->2); app-04 (4->3) is a
// separately approved baseline-reset preparation, never an implicit effect.
func validContractionTarget(node string, fromDesired, toDesired int) bool {
	return (node == App03 && fromDesired == 3 && toDesired == 2) ||
		(node == App04 && fromDesired == 4 && toDesired == 3)
}

func canonicalDropletAddress(node string) string {
	switch node {
	case App03:
		return App03DropletAddress
	case App04:
		return App04DropletAddress
	default:
		return ""
	}
}

func validDeleteActions(node string, actions []DeleteAction, dropletID, urn string) bool {
	if len(actions) == 0 {
		return false
	}
	address := canonicalDropletAddress(node)
	if address == "" {
		return false
	}
	seen := make(map[string]struct{}, len(actions))
	for _, action := range actions {
		if action.Address != address || action.Action != "delete" || action.DropletID != dropletID || action.DropletURN != urn {
			return false
		}
		if _, ok := seen[action.Address]; ok {
			return false
		}
		seen[action.Address] = struct{}{}
	}
	return true
}

// Digest is stable across retries and binds every supplied review fact.
func (c Claim) Digest() (string, error) {
	if c.Validate() != nil {
		return "", ErrInvalid
	}
	copy := c
	copy.DeleteActions = append([]DeleteAction(nil), c.DeleteActions...)
	copy.CreatedAt, copy.ExpiresAt = copy.CreatedAt.UTC(), copy.ExpiresAt.UTC()
	sort.Slice(copy.DeleteActions, func(i, j int) bool { return copy.DeleteActions[i].Address < copy.DeleteActions[j].Address })
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", ErrInvalid
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// ApplyGrant is the only authorizing response. Its durable creation consumes
// the claim before a runner can start tofu; a lost response is therefore
// ambiguous and cannot be replayed.
type ApplyGrant struct {
	SchemaVersion   string    `json:"schemaVersion"`
	ID              string    `json:"id"`
	ClaimID         string    `json:"claimId"`
	ClaimSHA256     string    `json:"claimSha256"`
	PlanID          string    `json:"planId"`
	AttemptID       string    `json:"attemptId"`
	Command         string    `json:"command"`
	AuthorizesApply bool      `json:"authorizesApply"`
	ConsumedAt      time.Time `json:"consumedAt"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

func (g ApplyGrant) Validate(c Claim) error {
	digest, err := c.Digest()
	if err != nil || g.SchemaVersion != GrantSchema || uuid.Validate(g.ID) != nil || g.ClaimID != c.ID || g.ClaimSHA256 != digest ||
		g.PlanID != c.PlanID || g.AttemptID != c.AttemptID || g.Command != ApplyCommand || !g.AuthorizesApply ||
		g.ConsumedAt.IsZero() || !g.ExpiresAt.After(g.ConsumedAt) || g.ExpiresAt.Sub(g.ConsumedAt) > MaxApplyGrantLifetime || g.ExpiresAt.After(c.ExpiresAt) {
		return ErrInvalid
	}
	return nil
}

// RefreshOnlyAuthorization is a record of a verifier-proved, read-only
// continuation. It contains neither an apply capability nor a way to mint one.
type RefreshOnlyAuthorization struct {
	SchemaVersion         string    `json:"schemaVersion"`
	ID                    string    `json:"id"`
	ClaimID               string    `json:"claimId"`
	PlanID                string    `json:"planId"`
	AttemptID             string    `json:"attemptId"`
	ProviderAbsenceSHA256 string    `json:"providerAbsenceSha256"`
	Command               string    `json:"command"`
	AuthorizesApply       bool      `json:"authorizesApply"`
	AuthorizedAt          time.Time `json:"authorizedAt"`
}

func (r RefreshOnlyAuthorization) Validate(c Claim) error {
	if r.SchemaVersion != RefreshOnlySchema || uuid.Validate(r.ID) != nil || r.ClaimID != c.ID || r.PlanID != c.PlanID ||
		r.AttemptID != c.AttemptID || !hex64.MatchString(r.ProviderAbsenceSHA256) || r.Command != RefreshOnlyCommand ||
		r.AuthorizesApply || r.AuthorizedAt.IsZero() {
		return ErrInvalid
	}
	return nil
}
