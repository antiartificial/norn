package fleetdeploy

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"

	"norn/v2/api/database"
	"norn/v2/api/etcdstore"
	"norn/v2/api/ingress"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

// ClaimedFleetRouteTransport contains private, worker-owned control transport
// material. Listener must bind a private address before the Nomad effect is
// advanced; the ingress listener is closed at the end of each claim attempt.
type ClaimedFleetRouteTransport struct {
	Listen           func() (net.Listener, error)
	NodeURIs         map[string]string
	AuthorityCertPEM []byte
	AuthorityKeyPEM  []byte
	NodeCAPEM        []byte
	PublisherCAPEM   []byte
	PublisherCertPEM []byte
	PublisherKeyPEM  []byte
	ObserverCAPEM    []byte
	ObserverCertPEM  []byte
	ObserverKeyPEM   []byte
	PublicRoots      *x509.CertPool
	ObserverPort     int
	EndpointPort     int
}

// ClaimedFleetDeploymentExecutor runs the signed first-route deployment under
// the normal operation worker's claim and app lock. Runtime construction must
// supply a private authority listener and separately scoped TLS identities.
type ClaimedFleetDeploymentExecutor struct {
	Store           *etcdstore.V3OperationStore
	Nomad           *nomad.Client
	DatabaseSecrets database.SecretSource
	JobSecrets      nomad.ManagedJobSecretSource
	AppsDir         string
	Authority       string
	Route           ClaimedFleetRouteTransport
}

func (e *ClaimedFleetDeploymentExecutor) ExecuteOperation(context.Context, *model.Operation, store.OperationClaim) (*pipeline.OperationResult, error) {
	return nil, fmt.Errorf("claimed Fleet deployment requires a worker app lock")
}

func (e *ClaimedFleetDeploymentExecutor) ExecuteOperationWithAppLock(ctx context.Context, op *model.Operation, claim store.OperationClaim,
	lock store.AppOperationLock) (*pipeline.OperationResult, error) {
	if e == nil || e.Store == nil || e.Nomad == nil || op == nil || op.Kind != "app.deploy" || op.ID != claim.OperationID() ||
		lock == nil || lock.Fence() == "" || lock.Context().Err() != nil || e.Route.Listen == nil || e.Authority == "" || e.AppsDir == "" {
		return nil, fmt.Errorf("claimed Fleet deployment executor is unavailable")
	}
	source, err := worker.LoadClaimedFleetDeploymentSource(ctx, e.Store, *op, e.AppsDir)
	if err != nil {
		return nil, err
	}
	databaseItems, err := worker.ResolveClaimedFleetRuntimeDatabaseItems(ctx, source, e.Store, e.DatabaseSecrets)
	if err != nil {
		return nil, err
	}
	plan, err := worker.PrepareClaimedFleetDeploymentJob(ctx, source, claim, e.Authority, e.Nomad, databaseItems, e.JobSecrets)
	if err != nil {
		return nil, err
	}
	listener, err := e.Route.Listen()
	if err != nil {
		return nil, err
	}
	if listener == nil {
		return nil, fmt.Errorf("claimed Fleet route authority listener is unavailable")
	}
	defer listener.Close()
	if e.Route.ObserverPort < 1024 || e.Route.ObserverPort > 65535 || e.Route.EndpointPort < 1 || e.Route.EndpointPort > 65535 || len(e.Route.NodeURIs) == 0 {
		return nil, fmt.Errorf("claimed Fleet route transport is incomplete")
	}
	if _, err := ingress.PrivateControlRouteAuthorityTLS(listener, e.Route.AuthorityCertPEM, e.Route.AuthorityKeyPEM, e.Route.NodeCAPEM); err != nil {
		return nil, err
	}
	effects, err := etcdstore.NewV3DeploymentEffectReservations(e.Store)
	if err != nil {
		return nil, err
	}
	healthEffect, err := worker.AdvanceClaimedFleetDeploymentJob(ctx, effects, e.Nomad, plan)
	if err != nil {
		return nil, err
	}
	serveCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()
	served := make(chan error, 1)
	go func() {
		served <- e.Store.ServeClaimedInitialFleetRouteAuthority(serveCtx, claim, lock, source.Spec,
			e.Route.ObserverPort, healthEffect, e.Route.NodeURIs, listener,
			e.Route.AuthorityCertPEM, e.Route.AuthorityKeyPEM, e.Route.NodeCAPEM)
	}()
	publication, publishErr := e.Store.PublishProveCompleteClaimedInitialFleetRoute(ctx, claim, lock, source.Spec, healthEffect,
		e.Route.ObserverPort, e.Route.EndpointPort, e.Route.PublisherCAPEM, e.Route.PublisherCertPEM, e.Route.PublisherKeyPEM,
		e.Route.ObserverCAPEM, e.Route.ObserverCertPEM, e.Route.ObserverKeyPEM, e.Route.PublicRoots)
	stopServing()
	_ = listener.Close()
	serveErr := <-served
	if publishErr != nil {
		return nil, publishErr
	}
	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		// Publication has already terminalized atomically. A listener shutdown
		// error cannot change that result, so report the durable receipt.
		log.Printf("claimed Fleet route authority shutdown after completed deployment: %v", serveErr)
	}
	if publication == nil || publication.Proof == nil || publication.Intent == nil {
		return nil, fmt.Errorf("claimed Fleet deployment completed without route proof")
	}
	return pipeline.FencedCompletedOperationResult(claim, "Fleet deployment healthy and ingress traffic proved", map[string]interface{}{
		"deploymentId":    source.Managed.Accepted.Deployment.ID,
		"routeIntentId":   publication.Intent.ID,
		"routeGeneration": publication.Intent.Generation,
	}), nil
}
