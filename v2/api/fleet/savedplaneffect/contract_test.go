package savedplaneffect

import (
	"crypto/ed25519"
	"crypto/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func claimFixture(now time.Time) Claim {
	id, plan, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString()
	return Claim{SchemaVersion: ClaimSchema, ID: id, PlanID: plan, PlanSHA256: strings.Repeat("a", 64), AttemptID: attempt,
		ProtectedCommit: strings.Repeat("b", 40), GitHubRunID: 1, GitHubRunAttempt: 1, GitHubJobID: 2, GitHubJobName: "apply", GitHubWorkflowRef: "org/repo/.github/workflows/work-staging.yml@" + strings.Repeat("b", 40), GitHubWorkflowSHA: strings.Repeat("b", 40), PlanRunID: 3, PlanArtifactSHA256: strings.Repeat("c", 64), DispatchNonceSHA256: strings.Repeat("d", 64), SavedBinaryPlanSHA256: strings.Repeat("e", 64), SavedJSONPlanSHA256: strings.Repeat("f", 64),
		PreparationProjectionSHA256: strings.Repeat("a", 64), PreparedPlanReceiptSHA256: strings.Repeat("b", 64),
		BackendKey: StagingBackendKey, StateLineage: "lineage", PreSerial: 19, WriterGeneration: 7, WriterHolder: attempt,
		TargetID: "staging/nyc3", TargetFenceGeneration: 9, AuthorityEpoch: 2, Node: App04, DropletID: "4242", DropletURN: "do:droplet:4242",
		DeleteActions: []DeleteAction{{Address: App04DropletAddress, Action: "delete", DropletID: "4242", DropletURN: "do:droplet:4242"}}, TransitionKind: StagingAppContraction, FromDesired: 4, ToDesired: 3,
		DrainReceiptSHA256: strings.Repeat("a", 64), VerifierAuthorizationSHA256: strings.Repeat("b", 64), CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
}

func TestProjectionIsCanonicalSignedAndOnlyExactContractions(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	claim := claimFixture(now)
	claim.GitHubJobName = "apply"
	claim.GitHubWorkflowRef = "antiartificial/norn-fleet/.github/workflows/apply.yml@refs/heads/main"
	projection, err := NewPreparationProjection(claim.PlanID, claim.PlanSHA256, claim.ProtectedCommit, DispatchBindingForClaim(claim), now)
	if err != nil || projection.Validate() != nil {
		t.Fatalf("projection=%+v err=%v", projection, err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewProjectionSigner(private)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.SignPreparation(projection)
	if err != nil || signed.Validate() != nil {
		t.Fatalf("signed=%+v err=%v", signed, err)
	}
	parsed, err := signed.Preparation()
	if err != nil || !reflect.DeepEqual(parsed, projection) {
		t.Fatalf("round trip=%+v err=%v", parsed, err)
	}
	changed := projection
	changed.GitHubJobName = "another-job"
	if changed.Validate() == nil {
		t.Fatal("accepted cross-node projection address")
	}
}

func TestClaimPinsOnlyExactApp04DeleteAndBoundedExpiry(t *testing.T) {
	now := time.Now().UTC()
	claim := claimFixture(now)
	if err := claim.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Claim){
		func(c *Claim) { c.Node = "app-03" },
		func(c *Claim) { c.BackendKey = "staging/nyc3/terraform.tfstate" },
		func(c *Claim) { c.DropletURN = "do:droplet:999" },
		func(c *Claim) { c.DeleteActions[0].Address = `module.cluster.digitalocean_droplet.node["app-04x"]` },
		func(c *Claim) { c.DeleteActions[0].Action = "update" },
		func(c *Claim) { c.DeleteActions = append(c.DeleteActions, c.DeleteActions[0]) },
		func(c *Claim) { c.ExpiresAt = c.CreatedAt.Add(MaxClaimLifetime + time.Second) },
	} {
		changed := claimFixture(now)
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatalf("accepted changed claim: %+v", changed)
		}
	}
}

func TestClaimPinsExactApp03ForMeasuredThreeToTwoContraction(t *testing.T) {
	now := time.Now().UTC()
	claim := claimFixture(now)
	claim.Node, claim.FromDesired, claim.ToDesired = App03, 3, 2
	claim.DropletID, claim.DropletURN = "610327215", "do:droplet:610327215"
	claim.DeleteActions = []DeleteAction{{Address: App03DropletAddress, Action: "delete", DropletID: claim.DropletID, DropletURN: claim.DropletURN}}
	if err := claim.Validate(); err != nil {
		t.Fatal(err)
	}
	claim.DeleteActions[0].Address = App04DropletAddress
	if err := claim.Validate(); err == nil {
		t.Fatal("accepted app-04 address for app-03 retirement")
	}
}

func TestApplyGrantBindsExactClaimAndNeverRefreshesApply(t *testing.T) {
	now := time.Now().UTC()
	claim := claimFixture(now)
	digest, err := claim.Digest()
	if err != nil {
		t.Fatal(err)
	}
	grant := ApplyGrant{SchemaVersion: GrantSchema, ID: uuid.NewString(), ClaimID: claim.ID, ClaimSHA256: digest, PlanID: claim.PlanID, AttemptID: claim.AttemptID, Command: ApplyCommand, AuthorizesApply: true, ConsumedAt: now, ExpiresAt: now.Add(time.Second)}
	if err := grant.Validate(claim); err != nil {
		t.Fatal(err)
	}
	refresh := RefreshOnlyAuthorization{SchemaVersion: RefreshOnlySchema, ID: uuid.NewString(), ClaimID: claim.ID, PlanID: claim.PlanID, AttemptID: claim.AttemptID, ProviderAbsenceSHA256: strings.Repeat("a", 64), Command: RefreshOnlyCommand, AuthorizedAt: now}
	if err := refresh.Validate(claim); err != nil {
		t.Fatal(err)
	}
	refresh.AuthorizesApply = true
	if refresh.Validate(claim) == nil {
		t.Fatal("refresh-only authorization granted apply")
	}
}
