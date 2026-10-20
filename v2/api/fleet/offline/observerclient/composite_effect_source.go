package observerclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"norn/v2/api/fleet/offline"
)

// The Nomad ID is a separate, owner-pinned work-staging identity. The signed
// dispatch binding presently contains only the provider droplet ID; treating
// a caller-supplied Nomad ID as equivalent would allow an identity swap.
const WorkApp03NomadNodeID = "fb10cefb-d416-363c-89b6-cb3a8247899a"

var ErrCompositeEffectSource = errors.New("independent effect sources disagree or are stale")

type effectOriginReader interface {
	ReadSource(context.Context, offline.EffectChallengeV1, offline.Binding, offline.Attempt) ([]byte, error)
}
type effectProviderReader interface {
	Observe(context.Context, offline.Binding) ([]byte, error)
}
type effectBackendReader interface {
	Read(context.Context, offline.EffectChallengeV1, offline.Binding) ([]byte, error)
}
type effectWorkloadReader interface {
	Observe(context.Context, time.Time, string, string) (offline.EffectWorkloadObservation, error)
}

// NomadConsulEffectReader runs the dedicated read-only workload observer.
type NomadConsulEffectReader struct{ Source offline.NomadConsulSource }

func (r NomadConsulEffectReader) Observe(ctx context.Context, now time.Time, nodeID, privateIP string) (offline.EffectWorkloadObservation, error) {
	return offline.ObserveEffectNomadConsul(ctx, r.Source, now, nodeID, privateIP)
}

// CompositeEffectSource composes four independent, read-only observations. It
// is intentionally not mounted on a route and never accepts a runner receipt.
// NomadNodeID must be the separately reviewed work-staging pin; rotation
// requires a source edit and review before another attempt can be observed.
type CompositeEffectSource struct {
	Origin      effectOriginReader
	Provider    effectProviderReader
	Backend     effectBackendReader
	Workload    effectWorkloadReader
	Clock       func() time.Time
	NomadNodeID string
}

func (s CompositeEffectSource) ObserveEffect(parent context.Context, c offline.EffectChallengeV1, b offline.Binding, a offline.Attempt) (EffectReads, error) {
	var zero EffectReads
	if parent == nil || parent.Err() != nil || s.Origin == nil || s.Provider == nil || s.Backend == nil || s.Workload == nil || s.Clock == nil ||
		s.NomadNodeID != WorkApp03NomadNodeID || b.ProviderAccountUUID != offline.WorkProviderAccountUUID {
		return zero, ErrCompositeEffectSource
	}
	start := s.Clock().UTC()
	if start.Before(c.IssuedAt) || !start.Before(c.ExpiresAt) || start.Sub(c.IssuedAt) > time.Minute ||
		c.Validate(b, a, c.AuthorityEpoch, start) != nil {
		return zero, ErrCompositeEffectSource
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	source, err := s.Origin.ReadSource(ctx, c, b, a)
	if err != nil || len(source) == 0 || len(source) > 1<<20 || !validSourceObservation(source, c, b, a, start) {
		return zero, ErrCompositeEffectSource
	}
	provider, err := s.Provider.Observe(ctx, b)
	if err != nil || len(provider) == 0 || len(provider) > 1<<20 {
		return zero, ErrCompositeEffectSource
	}
	privateIP, ok := validProviderObservation(provider, b)
	if !ok {
		return zero, ErrCompositeEffectSource
	}
	backend, err := s.Backend.Read(ctx, c, b)
	if err != nil || len(backend) == 0 || len(backend) > 16<<20 || !validBackendObservation(backend, c, b) {
		return zero, ErrCompositeEffectSource
	}
	work, err := s.Workload.Observe(ctx, s.Clock().UTC(), s.NomadNodeID, privateIP)
	if err != nil || len(work.RawEvidence) == 0 || len(work.RawEvidence) > 16<<20 || !validWorkloadObservation(work, s.NomadNodeID, privateIP) {
		return zero, ErrCompositeEffectSource
	}
	end := s.Clock().UTC()
	if ctx.Err() != nil || end.Before(start) || end.Sub(start) > 30*time.Second || end.Sub(c.IssuedAt) > time.Minute ||
		!end.Before(c.ExpiresAt) || end.Before(work.WorkloadEvidence.ObservedAt) || end.Sub(work.WorkloadEvidence.ObservedAt) > 30*time.Second ||
		c.Validate(b, a, c.AuthorityEpoch, end) != nil {
		return zero, ErrCompositeEffectSource
	}
	// Hash an unambiguous canonical envelope containing exact transcript hashes.
	evidence, err := json.Marshal(struct {
		Schema, ChallengeID, Source, Provider, Backend, Workload string
	}{"norn.fleet-composite-effect-evidence/v1", c.ID, digestEffectBytes(source), digestEffectBytes(provider), digestEffectBytes(backend), digestEffectBytes(work.RawEvidence)})
	if err != nil {
		return zero, ErrCompositeEffectSource
	}
	o := offline.EffectObservation{PlanID: b.PlanID, AttemptID: a.ID, BindingSHA256: a.BindingSHA256, TargetID: b.TargetID,
		SourceCommit: b.SourceCommit, SavedPlanSHA256: b.SavedPlanSHA256, EffectSHA256: b.EffectSHA256,
		Backend: b.Backend, ProviderAccountUUID: b.ProviderAccountUUID, NodeName: b.NodeName, DropletID: b.DropletID,
		FenceGeneration: b.FenceGeneration, AuthorityEpoch: c.AuthorityEpoch, ObservedAt: end,
		EvidenceSHA256: digestEffectBytes(evidence), TrustedOrigin: true, ProviderExact: true, LatestBackendExact: true,
		AllNamespacesInspected: work.WorkloadEvidence.AllNamespacesInspected, App03Empty: work.WorkloadEvidence.App03Empty,
		SurvivingPlacement: work.WorkloadEvidence.SurvivingPlacement, SurvivingHeadroom: work.WorkloadEvidence.SurvivingHeadroom, ServicePassing: work.WorkloadEvidence.ServicePassing}
	if offline.ValidateEffectObservation(o, b, a, c.AuthorityEpoch, end) != nil {
		return zero, ErrCompositeEffectSource
	}
	return EffectReads{Source: source, Provider: provider, Backend: backend, Workload: work.RawEvidence, Observation: o}, nil
}

func strictTranscript(raw []byte, dst any) bool {
	if !offlineTranscriptUnique(raw) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return false
	}
	return d.Decode(new(any)) == io.EOF
}

func offlineTranscriptUnique(raw []byte) bool {
	// Use the same recursive duplicate-key rejection used for provider JSON.
	return offlineUniqueJSON(raw)
}

// The provider helper is intentionally duplicated here in a small local form:
// nested duplicate keys in retained transcripts must fail, not last-write-win.
func offlineUniqueJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if !uniqueJSONValue(d, 0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
func uniqueJSONValue(d *json.Decoder, depth int) bool {
	if depth > 64 {
		return false
	}
	t, err := d.Token()
	if err != nil {
		return false
	}
	sep, ok := t.(json.Delim)
	if !ok {
		return true
	}
	switch sep {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			t, e := d.Token()
			key, ok := t.(string)
			if e != nil || !ok || seen[key] {
				return false
			}
			seen[key] = true
			if !uniqueJSONValue(d, depth+1) {
				return false
			}
		}
		t, err = d.Token()
		return err == nil && t == json.Delim('}')
	case '[':
		for d.More() {
			if !uniqueJSONValue(d, depth+1) {
				return false
			}
		}
		t, err = d.Token()
		return err == nil && t == json.Delim(']')
	default:
		return false
	}
}

func validSourceObservation(raw []byte, c offline.EffectChallengeV1, b offline.Binding, a offline.Attempt, start time.Time) bool {
	var x struct {
		Schema      string    `json:"schema"`
		ChallengeID string    `json:"challengeId"`
		PlanID      string    `json:"planId"`
		AttemptID   string    `json:"attemptId"`
		BindingSHA  string    `json:"bindingSHA256"`
		ObservedAt  time.Time `json:"observedAt"`
		GitHubProof struct {
			Schema        string `json:"schema"`
			Repository    string `json:"repository"`
			Branch        string `json:"branch"`
			Commit        string `json:"commit"`
			Protected     bool   `json:"protected"`
			ValidationRun int64  `json:"validationRun"`
		} `json:"githubProof"`
	}
	if !strictTranscript(raw, &x) || x.Schema != "norn.fleet-offline-effect-source-observation/v1" || x.ChallengeID != c.ID || x.PlanID != b.PlanID || x.AttemptID != a.ID || x.BindingSHA != a.BindingSHA256 ||
		x.GitHubProof.Schema != "norn.fleet-offline-effect-source/v1" || x.GitHubProof.Repository != "antiartificial/norn-fleet" || x.GitHubProof.Branch != "main" || x.GitHubProof.Commit != b.SourceCommit || !x.GitHubProof.Protected || x.GitHubProof.ValidationRun <= 0 {
		return false
	}
	return !x.ObservedAt.IsZero() && !x.ObservedAt.Before(start.Add(-30*time.Second)) && !x.ObservedAt.After(start.Add(30*time.Second))
}

func validProviderObservation(raw []byte, b offline.Binding) (string, bool) {
	var x struct {
		SchemaVersion string `json:"schemaVersion"`
		AccountUUID   string `json:"accountUUID"`
		ProjectID     string `json:"projectId"`
		VPCUUID       string `json:"vpcUUID"`
		Reads         []struct {
			Path      string          `json:"path"`
			Status    int             `json:"status"`
			RawSHA256 string          `json:"rawSHA256"`
			Fields    json.RawMessage `json:"fields"`
		} `json:"reads"`
		Nodes []struct {
			ID        uint64   `json:"id"`
			Name      string   `json:"name"`
			Status    string   `json:"status"`
			VPCUUID   string   `json:"vpc_uuid"`
			PrivateIP string   `json:"privateIP"`
			PublicIP  string   `json:"publicIP"`
			Tags      []string `json:"tags"`
		} `json:"nodes"`
	}
	if !strictTranscript(raw, &x) || x.SchemaVersion != "norn.fleet-provider-effect-transcript/v1" || x.AccountUUID != b.ProviderAccountUUID || x.ProjectID != offline.WorkProjectUUID || x.VPCUUID != offline.WorkVPCUUID || len(x.Reads) < 19 || len(x.Reads) > 25 || len(x.Nodes) != len(b.ProviderNodes) {
		return "", false
	}
	paths := map[string]int{}
	for _, read := range x.Reads {
		if read.Status != 200 || len(read.RawSHA256) != 64 || !json.Valid(read.Fields) || len(read.Fields) == 0 ||
			!(read.Path == "/v2/account" || strings.HasPrefix(read.Path, "/v2/projects/"+offline.WorkProjectUUID+"/resources?per_page=200&page=") || strings.HasPrefix(read.Path, "/v2/droplets/")) {
			return "", false
		}
		if _, err := hex.DecodeString(read.RawSHA256); err != nil {
			return "", false
		}
		paths[read.Path]++
	}
	if paths["/v2/account"] != 1 {
		return "", false
	}
	projectReads := 0
	for path, count := range paths {
		if strings.HasPrefix(path, "/v2/projects/") {
			page, err := strconv.Atoi(strings.TrimPrefix(path, "/v2/projects/"+offline.WorkProjectUUID+"/resources?per_page=200&page="))
			if err != nil || page < 1 || page > 4 || count != 2 {
				return "", false
			}
			projectReads += count
		}
	}
	if projectReads < 2 || projectReads > 8 || len(x.Reads) != 17+projectReads {
		return "", false
	}
	privateIP := ""
	for i, row := range x.Nodes {
		if paths["/v2/droplets/"+b.ProviderNodes[i].ProviderID] != 2 {
			return "", false
		}
		if strconv.FormatUint(row.ID, 10) != b.ProviderNodes[i].ProviderID || row.Name != "tk-staging-nyc3-"+b.ProviderNodes[i].Name || row.Status != "active" || row.VPCUUID != offline.WorkVPCUUID || row.PrivateIP == "" || row.PublicIP == "" {
			return "", false
		}
		if b.ProviderNodes[i].Name == offline.WorkNode {
			privateIP = row.PrivateIP
			if row.ID != 610327215 || privateIP != "10.43.0.13" || row.PublicIP != "159.203.100.16" {
				return "", false
			}
		}
	}
	return privateIP, privateIP != ""
}

func validBackendObservation(raw []byte, c offline.EffectChallengeV1, b offline.Binding) bool {
	var x struct {
		Schema              string          `json:"schema"`
		ChallengeID         string          `json:"challengeId"`
		ProviderAccountUUID string          `json:"providerAccountUUID"`
		Backend             offline.Backend `json:"backend"`
		RawState            []byte          `json:"rawState"`
		VersioningAndLatest string          `json:"versioningAndLatest"`
		OpenTofuLock        string          `json:"openTofuLock"`
	}
	if !strictTranscript(raw, &x) || x.Schema != "norn.fleet-effect-backend-transcript/v1" || x.ChallengeID != c.ID || x.ProviderAccountUUID != b.ProviderAccountUUID || x.Backend != b.Backend ||
		x.VersioningAndLatest != "verified-before-and-after" || x.OpenTofuLock != "authenticated-absent-before-and-after" || len(x.RawState) == 0 || len(x.RawState) > 8<<20 {
		return false
	}
	sum := sha256.Sum256(x.RawState)
	return hex.EncodeToString(sum[:]) == b.Backend.RawSHA256
}

func validWorkloadObservation(w offline.EffectWorkloadObservation, nodeID, privateIP string) bool {
	var x struct {
		Schema          string            `json:"schema"`
		TargetNodeID    string            `json:"targetNodeId"`
		TargetPrivateIP string            `json:"targetPrivateIp"`
		ObservedAt      time.Time         `json:"observedAt"`
		SnapshotHash    string            `json:"snapshotSHA256"`
		Reads           []json.RawMessage `json:"reads"`
	}
	return strictTranscript(w.RawEvidence, &x) && x.Schema == "norn.fleet-effect-workload-reads/v2" && x.TargetNodeID == nodeID && x.TargetPrivateIP == privateIP &&
		x.ObservedAt.Equal(w.WorkloadEvidence.ObservedAt) && x.SnapshotHash == w.WorkloadEvidence.SHA256 && len(x.Reads) > 0 && w.EvidenceSHA256 == digestEffectBytes(w.RawEvidence) &&
		w.WorkloadEvidence.AllNamespacesInspected && w.WorkloadEvidence.App03Empty && w.WorkloadEvidence.SurvivingPlacement && w.WorkloadEvidence.SurvivingHeadroom && w.WorkloadEvidence.ServicePassing
}
