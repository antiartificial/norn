package etcdstore

import (
	"context"
	"fmt"
	"net/http"

	"norn/v2/api/ingress"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// NewClaimedInitialFleetRouteAuthorityHandler binds a private mTLS listener to
// the worker's live deployment claim, app lock, and pinned source. No caller
// can supply route content or a node ID: the node ID comes from its verified
// certificate, and every request revalidates the durable control records.
// The worker must stop serving this handler when its claim or lock is lost.
func (s *V3OperationStore) NewClaimedInitialFleetRouteAuthorityHandler(claim store.OperationClaim, lock store.AppOperationLock, spec *model.InfraSpec, observerPort int, nodeURIs map[string]string) (http.Handler, error) {
	if s == nil || lock == nil || lock.Fence() == "" || lock.Context().Err() != nil || spec == nil || observerPort < 1024 || observerPort > 65535 {
		return nil, fmt.Errorf("claimed Fleet route authority is unavailable")
	}
	return ingress.NewControlRouteAuthorityHandler(nodeURIs, func(ctx context.Context, intentID, nodeID string) (*ingress.AuthorizedRoutePublication, error) {
		return s.AuthorizeInitialFleetRouteForNode(ctx, claim, lock, spec, observerPort, intentID, nodeID)
	})
}
