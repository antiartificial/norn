package githubapp

import (
	"context"
	"errors"
)

// VerifyOfflineEffectSource is a read-only, independently authenticated
// source check for the work-staging offline effect lane. The installation is
// fixed to the Fleet repository and protected main before reading GitHub.
// VerifyProtectedCommit additionally requires the exact SHA to be the current
// protected tip with a successful validate.yml push run at that SHA.
func (c *Client) VerifyOfflineEffectSource(ctx context.Context, sha string) error {
	if c == nil || c.cfg.Repository != "antiartificial/norn-fleet" || c.cfg.DefaultBranch != "main" {
		return errors.New("offline effect GitHub source is not the configured Fleet main")
	}
	proof, err := c.VerifyProtectedCommit(ctx, sha, "")
	if err != nil {
		return err
	}
	if proof == nil || proof.SHA != sha || proof.Verdict != CommitVerified || proof.ValidateRun <= 0 {
		return errors.New("offline effect Fleet main source is not independently verified")
	}
	return nil
}
