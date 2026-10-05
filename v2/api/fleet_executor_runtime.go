package main

// Wiring for the Fleet executor lane (docs/v3/fleet-controller/plan-5-7.md,
// work package E6, ADR 0010). It is shared by the two processes that serve
// Fleet: the PG authority-only router (main.go) and the etcd Fleet runtime
// (etcd_fleet_runtime.go). Neither the executor routes nor the converge
// watcher exist on the general router.
//
//   - fleetLaneAdmits is the scope matcher for the two lane principals: the
//     executor principal (exact fleet:execute) reaches exactly
//     executor.ExecutorRoutes, and the attempt-bound credential (fleet:attempt)
//     reaches exactly executor.AttemptCredentialRoutes through
//     handler.FleetAttemptCredentialAllowsRoute. Every other route refuses
//     them with 403 insufficient_scope.
//   - fleetExecutorScopeForRequest maps the new routes for every other
//     principal (provider evidence and execution mode need admin, approve
//     needs platform:operate, ...).
//   - The converge watcher starts only where the reconciler runs.
//   - The start-time VerifyProtectedCommit re-check is cached for 60 s per
//     (repository, commit, base).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/githubapp"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

// fleetExecutorCapability is the feature both Fleet processes advertise when
// the executor lane is mounted.
const fleetExecutorCapability = "fleet-executor-v1"

// fleetExecutorEnrollmentPlatform is the device platform that may enroll with
// fleet:execute (handler.FleetExecutorPlatform).
const fleetExecutorEnrollmentPlatform = handler.FleetExecutorPlatform

// withFleetExecutorCapabilities adds the executor lane's feature and
// endpoints. The authority's enrollmentScopes stay the ordinary operator
// scopes; fleet:execute is advertised per platform in
// enrollmentScopesByPlatform (it is grantable only alone and only to a
// fleet-executor device).
func withFleetExecutorCapabilities(features []string, endpoints map[string]string) ([]string, map[string]string) {
	features = append(features, fleetExecutorCapability)
	endpoints["fleetResource"] = "/api/v1/fleet/resources/{name}"
	endpoints["fleetResourceConverge"] = "/api/v1/fleet/resources/{name}/converge"
	endpoints["fleetResourceExecutionMode"] = "/api/v1/fleet/resources/{name}/execution-mode"
	endpoints["fleetConvergeApprove"] = "/api/v1/fleet/plans/{planID}/converge/approve"
	endpoints["fleetConvergeRefusals"] = "/api/v1/fleet/plans/{planID}/converge-refusals"
	endpoints["fleetCredentialGrants"] = "/api/v1/fleet/plans/{planID}/credential-grants"
	endpoints["fleetProviderEvidence"] = "/api/v1/fleet/plans/{planID}/provider-evidence"
	endpoints["fleetExecutorStopAttestations"] = "/api/v1/fleet/executor/stop-attestations"
	return features, endpoints
}

// fleetExecutorEnrollmentScopes is the capabilities auth.enrollmentScopesByPlatform value.
func fleetExecutorEnrollmentScopes() map[string][]string {
	return map[string][]string{fleetExecutorEnrollmentPlatform: {handler.ScopeFleetExecute}}
}

// --- scope matcher ---

type fleetLaneRoute struct {
	method string
	re     *regexp.Regexp
}

func compileFleetLaneRoutes(routes []string) []fleetLaneRoute {
	out := make([]fleetLaneRoute, 0, len(routes))
	placeholder := regexp.MustCompile(`\\\{[A-Za-z]+\\\}`)
	for _, route := range routes {
		method, path, _ := strings.Cut(route, " ")
		pattern := placeholder.ReplaceAllString(regexp.QuoteMeta(path), `[^/]+`)
		out = append(out, fleetLaneRoute{method: method, re: regexp.MustCompile("^" + pattern + "$")})
	}
	return out
}

var fleetExecutorLaneRoutes = compileFleetLaneRoutes(executor.ExecutorRoutes)

// fleetLaneSafePath refuses an escaped path (chi routes on the raw form, so
// the decoded path checked here would not be the route that serves it) and any
// dot segment.
func fleetLaneSafePath(r *http.Request) bool {
	if r.URL.RawPath != "" {
		return false
	}
	for _, segment := range strings.Split(r.URL.Path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// fleetExecutorRouteAllowed reports whether method and path are exactly one
// of executor.ExecutorRoutes.
func fleetExecutorRouteAllowed(method, path string) bool {
	for _, route := range fleetExecutorLaneRoutes {
		if route.method == method && route.re.MatchString(path) {
			return true
		}
	}
	return false
}

// fleetLaneAdmits is the matcher for the two lane principals. restricted is
// true when principal is the executor principal or the attempt-bound
// credential: such a principal is then decided here alone (allowed), and the
// generic scope check does not run for it.
func fleetLaneAdmits(principal *handler.AccessPrincipal, r *http.Request) (restricted, allowed bool) {
	if principal == nil {
		return false, false
	}
	if handler.IsFleetExecutorPrincipal(*principal) {
		return true, fleetLaneSafePath(r) && fleetExecutorRouteAllowed(r.Method, r.URL.Path)
	}
	if _, credential := handler.FleetAttemptCredentialAttemptID(*principal); credential {
		return true, fleetLaneSafePath(r) && handler.FleetAttemptCredentialAllowsRoute(*principal, r.Method, r.URL.Path)
	}
	for _, scope := range principal.Scopes {
		if scope == handler.ScopeFleetAttempt || scope == handler.ScopeFleetExecute {
			// Carries a lane scope but fails the lane principal's checks
			// (no live subject binding, a device or CI claim, another scope
			// beside fleet:execute, ...): never generic.
			return true, false
		}
	}
	return false, false
}

var (
	fleetPlanExecutorPathRe = regexp.MustCompile(`^/api/v1/fleet/plans/[^/]+/(converge/approve|converge-refusals|credential-grants|provider-evidence)$`)
	fleetResourceLanePathRe = regexp.MustCompile(`^/api/v1/fleet/resources/[^/]+/(converge|execution-mode)$`)
)

// fleetExecutorScopeForRequest maps the executor-lane routes for principals
// other than the two lane principals (which fleetLaneAdmits decides): the
// scope a route needs. ok is false for every other path.
//
//	POST resources/{name}/converge          platform:operate (the executor reaches it through the lane matcher)
//	POST resources/{name}/execution-mode    admin
//	POST plans/{planID}/converge/approve    platform:operate
//	POST plans/{planID}/provider-evidence   admin
//	POST plans/{planID}/credential-grants   fleet:execute
//	POST plans/{planID}/converge-refusals   fleet:execute
//	POST executor/stop-attestations         fleet:execute
//
// The handlers re-check their scope; for the three fleet:execute routes only
// the executor principal passes (admin reaches the handler and is refused
// there).
func fleetExecutorScopeForRequest(method, path string) (string, bool) {
	if method != http.MethodPost {
		return "", false
	}
	if path == "/api/v1/fleet/executor/stop-attestations" {
		return handler.ScopeFleetExecute, true
	}
	if match := fleetPlanExecutorPathRe.FindStringSubmatch(path); match != nil {
		switch match[1] {
		case "converge/approve":
			return handler.ScopePlatformOperate, true
		case "provider-evidence":
			return handler.ScopeAdmin, true
		default:
			return handler.ScopeFleetExecute, true
		}
	}
	if match := fleetResourceLanePathRe.FindStringSubmatch(path); match != nil {
		if match[1] == "execution-mode" {
			return handler.ScopeAdmin, true
		}
		return handler.ScopePlatformOperate, true
	}
	return "", false
}

// --- converge watcher ---

// fleetGitHubConfig is the Fleet GitHub App configuration of cfg.
func fleetGitHubConfig(cfg *config.Config) githubapp.Config {
	return githubapp.Config{AppID: cfg.FleetGitHubAppID, InstallationID: cfg.FleetGitHubInstallationID, PrivateKeyFile: cfg.FleetGitHubPrivateKeyFile, Repository: cfg.FleetGitHubRepository, Environment: cfg.FleetGitHubEnvironment, PilotRunID: cfg.FleetGitHubPilotRunID, DefaultBranch: cfg.FleetGitHubDefaultBranch, ConfigPath: cfg.FleetGitHubConfigPath, PlanWorkflow: cfg.FleetGitHubPlanWorkflow, ApplyWorkflow: cfg.FleetGitHubApplyWorkflow, RecoverWorkflow: cfg.FleetGitHubRecoverWorkflow, APIBaseURL: cfg.FleetGitHubAPIBaseURL, Production: cfg.Production()}
}

// fleetWatchedLister is the store read behind the watcher's source: both
// stores return controller-mode resources only (ListWatchedResources).
type fleetWatchedLister interface {
	ListWatchedResources(ctx context.Context) ([]controller.WatchedResource, error)
}

// fleetWatchedSource adapts a store's controller-mode listing to the one
// repository this process can verify: the E3 client is bound to
// cfg.FleetGitHubRepository, so a resource of another repository is skipped
// (the watcher could not verify its tip), and a resource with no desired
// revision yet (no repository recorded) belongs to the configured one.
type fleetWatchedSource struct {
	lister     fleetWatchedLister
	repository string
}

func (s fleetWatchedSource) ListWatchedResources(ctx context.Context) ([]controller.WatchedResource, error) {
	all, err := s.lister.ListWatchedResources(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]controller.WatchedResource, 0, len(all))
	for _, resource := range all {
		if resource.Repository == "" {
			resource.Repository = s.repository
		}
		if !strings.EqualFold(resource.Repository, s.repository) {
			continue
		}
		out = append(out, resource)
	}
	return out, nil
}

// fleetResourceDesiredStore is the resource read and CAS write the desired
// setter needs (*store.DB and *etcdstore.V3OperationStore).
type fleetResourceDesiredStore interface {
	GetFleetResource(ctx context.Context, name string) (*controller.Resource, error)
	SetDesiredFleetResourceIf(ctx context.Context, name string, expectedRevision int64, next controller.DesiredRevision) (*controller.Resource, error)
}

// fleetConvergeDesiredSetter accepts a recorded converge plan as the
// resource's desired revision (controller.ConvergeDesiredSetter). It is
// idempotent: a resource already at the plan is left alone, so the watcher's
// retry after a lost write heals. A plan whose generation is no longer the
// next one (an operator moved desired meanwhile) is refused loudly.
type fleetConvergeDesiredSetter struct {
	store fleetResourceDesiredStore
	now   func() time.Time
}

const fleetConvergeWatcherSubject = "fleet-converge-watcher"

func (s fleetConvergeDesiredSetter) SetConvergeDesired(ctx context.Context, watched controller.WatchedResource, plan controller.RecordedConverge) error {
	resource, err := s.store.GetFleetResource(ctx, watched.Name)
	if err != nil {
		return err
	}
	if resource.Desired.PlanID == plan.PlanID {
		return nil
	}
	if resource.Desired.Generation+1 != plan.Converge.Generation {
		return fmt.Errorf("converge plan %s is for generation %d but the resource is at generation %d", plan.PlanID, plan.Converge.Generation, resource.Desired.Generation)
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	_, err = s.store.SetDesiredFleetResourceIf(ctx, watched.Name, resource.Revision, controller.DesiredRevision{
		PlanID: plan.PlanID, CommitSHA: plan.Converge.CommitSHA, Repository: plan.Converge.Repository,
		Verification: controller.VerificationGitHubProtectedMain, AcceptedAt: now().UTC(), AcceptedBy: fleetConvergeWatcherSubject,
	})
	return err
}

// newFleetConvergeWatcher builds the converge watcher, or returns nil when
// this process does not reconcile (reconciler == nil) or has no GitHub App
// client: the watcher runs only where the reconciler runs (Q6, plan-5-7 2.4).
func newFleetConvergeWatcher(reconciler *controller.Reconciler, cfg *config.Config, github controller.CommitSource, lister fleetWatchedLister, recorder controller.ConvergePlanRecorder, desired fleetResourceDesiredStore) *controller.Watcher {
	if reconciler == nil || github == nil || lister == nil || recorder == nil {
		return nil
	}
	return &controller.Watcher{
		Source: fleetWatchedSource{lister: lister, repository: cfg.FleetGitHubRepository}, GitHub: github, Recorder: recorder,
		Desired:    fleetConvergeDesiredSetter{store: desired},
		SigningKey: cfg.AuditSigningKey,
		OnError: func(resource string, err error) {
			log.Printf("WARNING: fleet converge watcher resource=%q: %v", resource, err)
		},
	}
}

// startFleetConvergeWatcher runs w until ctx is cancelled. The returned stop
// function cancels it and waits for Run to return.
func startFleetConvergeWatcher(ctx context.Context, w *controller.Watcher) (stop func()) {
	runCtx, cancel := context.WithCancel(ctx)
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		w.Run(runCtx)
	}()
	return func() {
		cancel()
		done.Wait()
	}
}

// fleetUnavailableRescanner answers the rescan route on a process that runs
// no watcher.
type fleetUnavailableRescanner struct{}

func (fleetUnavailableRescanner) Rescan(context.Context, string) (controller.WatchDecision, error) {
	return controller.WatchDecision{}, errors.New("this process runs no converge watcher")
}

// --- start-time verification ---

// fleetStartVerifierTTL bounds how long a verified verdict is reused (plan
// section 2.4: a cache of 60 s or less).
const fleetStartVerifierTTL = 60 * time.Second

type fleetCommitVerifier interface {
	VerifyProtectedCommit(ctx context.Context, sha, lastApplied string) (*githubapp.CommitVerification, error)
}

// fleetStartVerifier re-verifies a plan's commit at executor attempt start
// with the E3 client of the plan's repository. It implements
// handler.FleetExecutorStartVerifier. Only a verified verdict is cached, so a
// pending or failed one is re-read on the next start.
type fleetStartVerifier struct {
	clients map[string]fleetCommitVerifier // lower-cased "owner/name"
	ttl     time.Duration
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]fleetStartVerdict
}

type fleetStartVerdict struct {
	at           time.Time
	verification githubapp.CommitVerification
}

func newFleetStartVerifier(repository string, client fleetCommitVerifier) *fleetStartVerifier {
	return &fleetStartVerifier{clients: map[string]fleetCommitVerifier{strings.ToLower(repository): client}, ttl: fleetStartVerifierTTL, now: time.Now, cache: map[string]fleetStartVerdict{}}
}

func (v *fleetStartVerifier) VerifyStartCommit(ctx context.Context, repository, commitSHA, lastApplied string) (*githubapp.CommitVerification, error) {
	client := v.clients[strings.ToLower(repository)]
	if client == nil {
		return &githubapp.CommitVerification{SHA: commitSHA, Verdict: githubapp.CommitUnverified, Code: executor.CodeFleetExecutorTriggerUnverified, Reason: "no GitHub App client is configured for repository " + repository}, nil
	}
	key := strings.ToLower(repository) + "\x00" + commitSHA + "\x00" + lastApplied
	v.mu.Lock()
	if cached, ok := v.cache[key]; ok && v.now().Sub(cached.at) < v.ttl {
		verification := cached.verification
		v.mu.Unlock()
		return &verification, nil
	}
	v.mu.Unlock()
	verification, err := client.VerifyProtectedCommit(ctx, commitSHA, lastApplied)
	if err != nil {
		return nil, err
	}
	if verification.Verdict == githubapp.CommitVerified {
		v.mu.Lock()
		now := v.now()
		for k, cached := range v.cache {
			if now.Sub(cached.at) >= v.ttl {
				delete(v.cache, k)
			}
		}
		v.cache[key] = fleetStartVerdict{at: now, verification: *verification}
		v.mu.Unlock()
	}
	return verification, nil
}

// --- etcd route authentication ---

// etcdFleetLaneAuth is etcdManagedTokenAuth plus the lane matcher: the
// executor principal and the attempt-bound credential are decided by
// fleetLaneAdmits alone (their exact route sets), every other principal needs
// one of requiredScopes (any managed principal when none are given). It is
// mounted on each etcd route in executor.ExecutorRoutes, and on the
// self-service auth routes, so a lane principal never reaches a route outside
// its set.
func etcdFleetLaneAuth(cfg *config.Config, identities store.IdentityStore, requiredScopes ...string) func(http.Handler) http.Handler {
	return etcdFleetLaneAuthGated(cfg, identities, true, requiredScopes...)
}

// etcdFleetLaneAuthGated is etcdFleetLaneAuth; with laneOpen false the two
// lane principals are refused on the route whatever their route sets say
// (etcdFleetExecutorLaneEnabled).
func etcdFleetLaneAuthGated(cfg *config.Config, identities store.IdentityStore, laneOpen bool, requiredScopes ...string) func(http.Handler) http.Handler {
	scoped := etcdManagedTokenAuth(cfg, identities, requiredScopes...)
	return func(next http.Handler) http.Handler {
		scopedNext := scoped(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			principal, ok := handler.VerifyAccessTokenWithIdentityStore(cfg.APIToken, value, cfg.LegacyTokenSigningUntil, identities)
			if !ok || principal.Source != handler.AccessPrincipalSourceManagedToken {
				handler.WriteControlProblem(w, r, http.StatusUnauthorized, "unauthorized", "a managed etcd access token is required")
				return
			}
			if restricted, allowed := fleetLaneAdmits(principal, r); restricted {
				if !allowed || !laneOpen {
					handler.WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "this credential may not call this route")
					return
				}
				next.ServeHTTP(w, handler.WithAccessPrincipal(r, principal))
				return
			}
			scopedNext.ServeHTTP(w, r)
		})
	}
}

// etcdFleetExecutorLane carries what the etcd executor lane adds to the
// resource routes: the watcher's decisions (nil without a watcher).
type etcdFleetExecutorLane struct {
	decisions handler.ConvergeDecisionSource
	devices   store.AuthStore
}

// registerEtcdFleetExecutorRoutes mounts the executor lane on the etcd
// runtime router: the converge rescan and approve routes, the E5 routes
// (grants, stop attestations, provider evidence, converge refusals) and the
// execution-mode route is mounted with the resource routes. The executor
// attempt, heartbeat, advance, checkpoint and resource-read routes are the
// existing ones, re-authenticated with etcdFleetLaneAuth.
//
// Enrollment and DELETE /devices/{id} are NOT served by the etcd runtime:
// the enrollment handlers are bound to the PostgreSQL store. An executor
// device is enrolled and revoked only through the PG authority; until the v3
// executor phase profile exists (P2/P3 hand-off) the etcd executor lane has no
// enrolled principal and answers 401/403 to every executor call.
func registerEtcdFleetExecutorRoutes(router chi.Router, cfg *config.Config, identities *etcdstore.AuthStore, operations *etcdstore.V3OperationStore, rescanner handler.ConvergeRescanner) {
	converge := handler.NewEtcdFleetConvergeRoutes(cfg, operations, rescanner)
	ops := handler.NewEtcdFleetExecutorOpsRoutes(cfg, operations, identities)
	operate := etcdFleetLaneAuth(cfg, identities, handler.ScopePlatformOperate)
	execute := etcdFleetLaneAuth(cfg, identities, handler.ScopeFleetExecute)
	admin := etcdFleetLaneAuth(cfg, identities, handler.ScopeAdmin)
	router.With(operate).Post("/api/v1/fleet/resources/{name}/converge", converge.Rescan)
	router.With(operate).Post("/api/v1/fleet/plans/{planID}/converge/approve", converge.Approve)
	router.With(execute).Post("/api/v1/fleet/plans/{planID}/credential-grants", ops.Grant)
	router.With(execute).Post("/api/v1/fleet/plans/{planID}/converge-refusals", ops.Refuse)
	router.With(execute).Post("/api/v1/fleet/executor/stop-attestations", ops.StopAttestation)
	router.With(admin).Post("/api/v1/fleet/plans/{planID}/provider-evidence", ops.ProviderEvidence)
}
