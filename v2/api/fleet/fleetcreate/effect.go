// Package fleetcreate defines Norn's pre-registration Fleet-create authority
// contract. It contains no provider client and cannot perform an apply.
package fleetcreate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

const (
	IntentSchema        = "norn.fleet-create-intent/v1"
	DispatchSchema      = "norn.fleet-create-dispatch-binding/v1"
	AttemptSchema       = "norn.fleet-create-attempt/v1"
	StartSchema         = "norn.fleet-create-attempt-start/v1"
	AdmissionSchema     = "norn.fleet-create-admission/v1"
	RecoverySchema      = "norn.fleet-create-recovery-readback/v1"
	TerminalSchema      = "norn.fleet-create-terminal-receipt/v1"
	RequestSchema       = "norn.fleet-create-effect-request/v1"
	GrantSchema         = "norn.fleet-create-effect-grant/v1"
	intentIDPattern     = `^[a-z0-9][a-z0-9-]{0,62}$`
	repositoryPattern   = `^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`
	workflowPathPattern = `^\.github/workflows/[A-Za-z0-9_.-]+\.ya?ml$`
	githubRunIDPattern  = `^[1-9][0-9]{0,19}$`
)

// DispatchBinding is the independent verifier's durable readback of one
// protected GitHub workflow dispatch. Norn binds the exact runner, source,
// and receipt digest; the route authenticating the workflow must compare its
// OIDC claims with these fields before calling StartFleetCreateAttempt.
type DispatchBinding struct {
	SchemaVersion         string `json:"schemaVersion"`
	IntentID              string `json:"intentId"`
	PlanID                string `json:"planId"`
	Cluster               string `json:"cluster"`
	IntentSHA256          string `json:"intentSha256"`
	Repository            string `json:"repository"`
	RepositoryID          string `json:"repositoryId"`
	RepositoryOwnerID     string `json:"repositoryOwnerId"`
	WorkflowPath          string `json:"workflowPath"`
	WorkflowSHA           string `json:"workflowSha"`
	Environment           string `json:"environment"`
	RunID                 string `json:"runId"`
	RunAttempt            string `json:"runAttempt"`
	CommitSHA             string `json:"commitSha"`
	DispatchReceiptSHA256 string `json:"dispatchReceiptSha256"`
}

func (d DispatchBinding) Validate() error {
	if d.SchemaVersion != DispatchSchema || uuid.Validate(d.IntentID) != nil || uuid.Validate(d.PlanID) != nil ||
		!clusterPattern.MatchString(d.Cluster) || !sha256Pattern.MatchString(d.IntentSHA256) ||
		!repositoryRe.MatchString(d.Repository) || !workflowPathRe.MatchString(d.WorkflowPath) ||
		!githubRunIDRe.MatchString(d.RepositoryID) || !githubRunIDRe.MatchString(d.RepositoryOwnerID) ||
		!commitPattern.MatchString(d.WorkflowSHA) ||
		d.Environment != "work-staging" || !githubRunIDRe.MatchString(d.RunID) ||
		!githubRunIDRe.MatchString(d.RunAttempt) ||
		!commitPattern.MatchString(d.CommitSHA) || !sha256Pattern.MatchString(d.DispatchReceiptSHA256) {
		return ErrInvalid
	}
	return nil
}

func (d DispatchBinding) MatchesIntent(i Intent) bool {
	digest, err := i.Digest()
	return err == nil && d.Validate() == nil && d.IntentID == i.IntentID && d.PlanID == i.PlanID &&
		d.Cluster == i.Cluster && d.IntentSHA256 == digest && d.CommitSHA == i.CommitSHA
}

func (d DispatchBinding) RunnerAttemptID() string {
	if d.Validate() != nil {
		return ""
	}
	return "github-actions:" + d.Repository + ":" + d.RunID + ":" + d.RunAttempt
}

func (d DispatchBinding) MatchesStart(r AttemptStartRequest) bool {
	return r.Validate() == nil && d.Validate() == nil && r.IntentID == d.IntentID && r.PlanID == d.PlanID &&
		r.Cluster == d.Cluster && r.IntentSHA256 == d.IntentSHA256 &&
		r.RunnerAttemptID == d.RunnerAttemptID() && r.DispatchReceiptSHA256 == d.DispatchReceiptSHA256
}

func (d DispatchBinding) Digest() string {
	raw, err := json.Marshal(d)
	if err != nil {
		return ""
	}
	return SHA256(raw)
}

// AttemptStartRequest binds the initial pre-registration writer to a saved
// intent and protected dispatch receipt. It does not itself authorize an
// effect; that requires a separate readback admission and one-use grant.
type AttemptStartRequest struct {
	SchemaVersion         string `json:"schemaVersion"`
	IntentID              string `json:"intentId"`
	PlanID                string `json:"planId"`
	Cluster               string `json:"cluster"`
	AttemptID             string `json:"attemptId"`
	IntentSHA256          string `json:"intentSha256"`
	RunnerAttemptID       string `json:"runnerAttemptId"`
	DispatchReceiptSHA256 string `json:"dispatchReceiptSha256"`
}

func (r AttemptStartRequest) Validate() error {
	if r.SchemaVersion != StartSchema || uuid.Validate(r.IntentID) != nil || uuid.Validate(r.PlanID) != nil ||
		!clusterPattern.MatchString(r.Cluster) ||
		uuid.Validate(r.AttemptID) != nil || !sha256Pattern.MatchString(r.IntentSHA256) ||
		len(r.RunnerAttemptID) < 1 || len(r.RunnerAttemptID) > 128 ||
		!sha256Pattern.MatchString(r.DispatchReceiptSHA256) {
		return ErrInvalid
	}
	return nil
}

type Attempt struct {
	SchemaVersion   string `json:"schemaVersion"`
	IntentID        string `json:"intentId"`
	PlanID          string `json:"planId"`
	AttemptID       string `json:"attemptId"`
	IntentSHA256    string `json:"intentSha256"`
	RequestSHA256   string `json:"requestSha256"`
	RunnerAttemptID string `json:"runnerAttemptId"`
	Generation      uint64 `json:"generation"`
	Status          string `json:"status"`
}

func AttemptFromStart(r AttemptStartRequest) Attempt {
	return AttemptForGeneration(r, 1)
}

func AttemptForGeneration(r AttemptStartRequest, generation uint64) Attempt {
	raw, _ := json.Marshal(r)
	return Attempt{SchemaVersion: AttemptSchema, IntentID: r.IntentID, PlanID: r.PlanID,
		AttemptID: r.AttemptID, IntentSHA256: r.IntentSHA256, RequestSHA256: SHA256(raw),
		RunnerAttemptID: r.RunnerAttemptID, Generation: generation, Status: "admitted"}
}

func (a Attempt) MatchesStart(r AttemptStartRequest) bool {
	want := AttemptForGeneration(r, a.Generation)
	return r.Validate() == nil && a.Generation > 0 && a == want
}

var (
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	clusterPattern = regexp.MustCompile(intentIDPattern)
	repositoryRe   = regexp.MustCompile(repositoryPattern)
	workflowPathRe = regexp.MustCompile(workflowPathPattern)
	githubRunIDRe  = regexp.MustCompile(githubRunIDPattern)
	ErrInvalid     = errors.New("fleet create effect contract is invalid")
)

// Intent binds the pre-registration operation to reviewed inputs. Provider
// account, backend, lineage, topology, and SSH values are represented only by
// digests; they are opaque to Norn and must be established by the independent
// Fleet verifier.
type Intent struct {
	SchemaVersion         string `json:"schemaVersion"`
	IntentID              string `json:"intentId"`
	PlanID                string `json:"planId"`
	Cluster               string `json:"cluster"`
	CommitSHA             string `json:"commitSha"`
	PlanSHA256            string `json:"planSha256"`
	ProviderAccountSHA256 string `json:"providerAccountSha256"`
	StateBackendSHA256    string `json:"stateBackendSha256"`
	StateLineageSHA256    string `json:"stateLineageSha256"`
	StateSerial           uint64 `json:"stateSerial"`
	TopologySHA256        string `json:"topologySha256"`
	SSHInputsSHA256       string `json:"sshInputsSha256"`
}

func (i Intent) Validate() error {
	if i.SchemaVersion != IntentSchema || uuid.Validate(i.IntentID) != nil ||
		uuid.Validate(i.PlanID) != nil || !clusterPattern.MatchString(i.Cluster) ||
		!commitPattern.MatchString(i.CommitSHA) || !sha256Pattern.MatchString(i.PlanSHA256) ||
		!sha256Pattern.MatchString(i.ProviderAccountSHA256) || !sha256Pattern.MatchString(i.StateBackendSHA256) ||
		!sha256Pattern.MatchString(i.StateLineageSHA256) || i.StateSerial == 0 ||
		!sha256Pattern.MatchString(i.TopologySHA256) || !sha256Pattern.MatchString(i.SSHInputsSHA256) {
		return ErrInvalid
	}
	return nil
}

func (i Intent) Digest() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(i)
	if err != nil {
		return "", err
	}
	return SHA256(raw), nil
}

// Admission records verifier-owned exact readback for one attempt and the
// exact provider remainder it observed. Norn validates shape and immutable
// binding; it does not interpret or vouch for the provider/state digests.
type Admission struct {
	SchemaVersion          string `json:"schemaVersion"`
	IntentID               string `json:"intentId"`
	PlanID                 string `json:"planId"`
	Cluster                string `json:"cluster"`
	AttemptID              string `json:"attemptId"`
	IntentSHA256           string `json:"intentSha256"`
	RemainderSHA256        string `json:"remainderSha256"`
	ProviderReadbackSHA256 string `json:"providerReadbackSha256"`
	StateReadbackSHA256    string `json:"stateReadbackSha256"`
	StateLineageSHA256     string `json:"stateLineageSha256"`
	StateSerial            uint64 `json:"stateSerial"`
	ExpectedNodeCount      uint32 `json:"expectedNodeCount"`
	ObservedNodeCount      uint32 `json:"observedNodeCount"`
}

// RecoveryReadback is verifier evidence that a prior attempt's provider and
// state effects are fully understood and a precise remainder is safe to
// resume. Norn checks binding and shape but cannot validate provider facts.
type RecoveryReadback struct {
	SchemaVersion          string `json:"schemaVersion"`
	IntentID               string `json:"intentId"`
	PlanID                 string `json:"planId"`
	Cluster                string `json:"cluster"`
	PriorAttemptID         string `json:"priorAttemptId"`
	IntentSHA256           string `json:"intentSha256"`
	PriorGrantSHA256       string `json:"priorGrantSha256"`
	ProviderReadbackSHA256 string `json:"providerReadbackSha256"`
	StateReadbackSHA256    string `json:"stateReadbackSha256"`
	StateLineageSHA256     string `json:"stateLineageSha256"`
	StateSerial            uint64 `json:"stateSerial"`
	ExpectedNodeCount      uint32 `json:"expectedNodeCount"`
	ObservedNodeCount      uint32 `json:"observedNodeCount"`
	RemainderSHA256        string `json:"remainderSha256"`
}

// TerminalReceipt binds the final complete provider/state inventory to the
// consumed final-attempt grant and the exact target identity Norn will register.
type TerminalReceipt struct {
	SchemaVersion          string `json:"schemaVersion"`
	IntentID               string `json:"intentId"`
	PlanID                 string `json:"planId"`
	Cluster                string `json:"cluster"`
	AttemptID              string `json:"attemptId"`
	IntentSHA256           string `json:"intentSha256"`
	AdmissionSHA256        string `json:"admissionSha256"`
	GrantSHA256            string `json:"grantSha256"`
	ProviderReadbackSHA256 string `json:"providerReadbackSha256"`
	StateReadbackSHA256    string `json:"stateReadbackSha256"`
	NodeIdentitySetSHA256  string `json:"nodeIdentitySetSha256"`
	StateLineageSHA256     string `json:"stateLineageSha256"`
	StateSerial            uint64 `json:"stateSerial"`
	ExpectedNodeCount      uint32 `json:"expectedNodeCount"`
	ObservedNodeCount      uint32 `json:"observedNodeCount"`
	Provider               string `json:"provider"`
	ProviderAccount        string `json:"providerAccount"`
	StateBackend           string `json:"stateBackend"`
}

func (r TerminalReceipt) Validate() error {
	if r.SchemaVersion != TerminalSchema || uuid.Validate(r.IntentID) != nil || uuid.Validate(r.PlanID) != nil ||
		!clusterPattern.MatchString(r.Cluster) || uuid.Validate(r.AttemptID) != nil ||
		!sha256Pattern.MatchString(r.IntentSHA256) || !sha256Pattern.MatchString(r.AdmissionSHA256) ||
		!sha256Pattern.MatchString(r.GrantSHA256) || !sha256Pattern.MatchString(r.ProviderReadbackSHA256) ||
		!sha256Pattern.MatchString(r.StateReadbackSHA256) || !sha256Pattern.MatchString(r.NodeIdentitySetSHA256) ||
		!sha256Pattern.MatchString(r.StateLineageSHA256) || r.StateSerial == 0 || r.ExpectedNodeCount != 9 ||
		r.ObservedNodeCount != r.ExpectedNodeCount || strings.TrimSpace(r.Provider) == "" ||
		strings.TrimSpace(r.ProviderAccount) == "" || strings.TrimSpace(r.StateBackend) == "" {
		return ErrInvalid
	}
	return nil
}

func (r TerminalReceipt) Digest() string {
	raw, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return SHA256(raw)
}

func (r TerminalReceipt) MatchesIntent(i Intent) bool {
	digest, err := i.Digest()
	return err == nil && r.Validate() == nil && r.IntentID == i.IntentID && r.PlanID == i.PlanID && r.Cluster == i.Cluster &&
		r.IntentSHA256 == digest && r.StateLineageSHA256 == i.StateLineageSHA256 && r.StateSerial >= i.StateSerial &&
		SHA256([]byte(r.ProviderAccount)) == i.ProviderAccountSHA256 && SHA256([]byte(r.StateBackend)) == i.StateBackendSHA256
}

func (r RecoveryReadback) Validate() error {
	if r.SchemaVersion != RecoverySchema || uuid.Validate(r.IntentID) != nil || uuid.Validate(r.PlanID) != nil ||
		!clusterPattern.MatchString(r.Cluster) || uuid.Validate(r.PriorAttemptID) != nil ||
		!sha256Pattern.MatchString(r.IntentSHA256) || !sha256Pattern.MatchString(r.PriorGrantSHA256) ||
		!sha256Pattern.MatchString(r.ProviderReadbackSHA256) || !sha256Pattern.MatchString(r.StateReadbackSHA256) ||
		!sha256Pattern.MatchString(r.StateLineageSHA256) || r.StateSerial == 0 || r.ExpectedNodeCount == 0 ||
		r.ObservedNodeCount >= r.ExpectedNodeCount || !sha256Pattern.MatchString(r.RemainderSHA256) {
		return ErrInvalid
	}
	return nil
}

func (r RecoveryReadback) Digest() string {
	raw, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return SHA256(raw)
}

func (r RecoveryReadback) MatchesIntent(i Intent) bool {
	digest, err := i.Digest()
	return err == nil && r.Validate() == nil && r.IntentID == i.IntentID && r.PlanID == i.PlanID &&
		r.Cluster == i.Cluster && r.IntentSHA256 == digest && r.StateLineageSHA256 == i.StateLineageSHA256 &&
		r.StateSerial >= i.StateSerial
}

func (a Admission) Validate() error {
	if a.SchemaVersion != AdmissionSchema || uuid.Validate(a.IntentID) != nil ||
		uuid.Validate(a.PlanID) != nil || !clusterPattern.MatchString(a.Cluster) || uuid.Validate(a.AttemptID) != nil ||
		!sha256Pattern.MatchString(a.IntentSHA256) || !sha256Pattern.MatchString(a.RemainderSHA256) ||
		!sha256Pattern.MatchString(a.ProviderReadbackSHA256) || !sha256Pattern.MatchString(a.StateReadbackSHA256) ||
		!sha256Pattern.MatchString(a.StateLineageSHA256) || a.StateSerial == 0 || a.ExpectedNodeCount == 0 ||
		a.ObservedNodeCount >= a.ExpectedNodeCount {
		return ErrInvalid
	}
	return nil
}

func (a Admission) MatchesIntent(i Intent) bool {
	digest, err := i.Digest()
	return err == nil && a.Validate() == nil && a.IntentID == i.IntentID &&
		a.PlanID == i.PlanID && a.Cluster == i.Cluster && a.IntentSHA256 == digest &&
		a.StateLineageSHA256 == i.StateLineageSHA256 && a.StateSerial >= i.StateSerial
}

type Request struct {
	SchemaVersion   string `json:"schemaVersion"`
	IntentID        string `json:"intentId"`
	PlanID          string `json:"planId"`
	AttemptID       string `json:"attemptId"`
	IntentSHA256    string `json:"intentSha256"`
	AdmissionSHA256 string `json:"admissionSha256"`
	RemainderSHA256 string `json:"remainderSha256"`
}

func (r Request) Validate() error {
	if r.SchemaVersion != RequestSchema || uuid.Validate(r.IntentID) != nil ||
		uuid.Validate(r.PlanID) != nil || uuid.Validate(r.AttemptID) != nil ||
		!sha256Pattern.MatchString(r.IntentSHA256) || !sha256Pattern.MatchString(r.AdmissionSHA256) ||
		!sha256Pattern.MatchString(r.RemainderSHA256) {
		return ErrInvalid
	}
	return nil
}

func (r Request) MatchesAdmission(a Admission) bool {
	return r.Validate() == nil && a.Validate() == nil && r.IntentID == a.IntentID &&
		r.PlanID == a.PlanID && r.AttemptID == a.AttemptID && r.IntentSHA256 == a.IntentSHA256 &&
		r.AdmissionSHA256 == a.Digest() && r.RemainderSHA256 == a.RemainderSHA256
}

func (a Admission) Digest() string {
	raw, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	return SHA256(raw)
}

type Grant struct {
	SchemaVersion    string `json:"schemaVersion"`
	AuthorizesEffect bool   `json:"authorizesEffect"`
	IntentID         string `json:"intentId"`
	PlanID           string `json:"planId"`
	AttemptID        string `json:"attemptId"`
	IntentSHA256     string `json:"intentSha256"`
	AdmissionSHA256  string `json:"admissionSha256"`
	RemainderSHA256  string `json:"remainderSha256"`
}

// GrantReadback reports the immutable grant and whether its durable one-use
// consumption event exists. It is authority state, not provider evidence.
type GrantReadback struct {
	Grant    Grant `json:"grant"`
	Consumed bool  `json:"consumed"`
}

// GrantStatus is the runner-safe readback after an ambiguous request. It omits
// the grant record so a status check cannot look like a second successful
// consume response.
type GrantStatus struct {
	IntentID    string `json:"intentId"`
	AttemptID   string `json:"attemptId"`
	GrantSHA256 string `json:"grantSha256"`
	Consumed    bool   `json:"consumed"`
}

func GrantFromRequest(r Request) Grant {
	return Grant{SchemaVersion: GrantSchema, AuthorizesEffect: true,
		IntentID: r.IntentID, PlanID: r.PlanID, AttemptID: r.AttemptID,
		IntentSHA256: r.IntentSHA256, AdmissionSHA256: r.AdmissionSHA256,
		RemainderSHA256: r.RemainderSHA256}
}

func (g Grant) MatchesRequest(r Request) bool {
	return r.Validate() == nil && g.SchemaVersion == GrantSchema && g.AuthorizesEffect &&
		g.IntentID == r.IntentID && g.PlanID == r.PlanID && g.AttemptID == r.AttemptID &&
		g.IntentSHA256 == r.IntentSHA256 && g.AdmissionSHA256 == r.AdmissionSHA256 &&
		g.RemainderSHA256 == r.RemainderSHA256
}

func (g Grant) Digest() string {
	raw, err := json.Marshal(g)
	if err != nil {
		return ""
	}
	return SHA256(raw)
}

// DecodeRequest rejects duplicate keys, unknown fields, and trailing JSON.
func DecodeRequest(raw []byte) (Request, error) {
	var request Request
	if len(raw) == 0 || len(raw) > 16*1024 || !json.Valid(raw) || duplicateJSONKey(json.NewDecoder(bytes.NewReader(raw))) {
		return request, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || request.Validate() != nil {
		return Request{}, ErrInvalid
	}
	return request, nil
}

func duplicateJSONKey(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return true
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return true
		}
		if _, exists := seen[key]; exists {
			return true
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return true
		}
	}
	_, err = decoder.Token()
	return err != nil
}

// SHA256 returns the lowercase hex digest used by this package's contracts.
func SHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
