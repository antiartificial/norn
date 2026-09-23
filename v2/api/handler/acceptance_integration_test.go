package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/config"
	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

func acceptanceIntegrationDB(t *testing.T) *store.DB {
	t.Helper()
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(context.Background(), adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schemaName := "handler_acceptance_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE") })

	testConfig := adminConfig.Copy()
	if testConfig.ConnConfig.RuntimeParams == nil {
		testConfig.ConnConfig.RuntimeParams = map[string]string{}
	}
	testConfig.ConnConfig.RuntimeParams["search_path"] = schemaName
	if testConfig.MaxConns < 4 {
		testConfig.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), testConfig)
	if err != nil {
		t.Fatal(err)
	}
	db := &store.DB{Pool: pool}
	t.Cleanup(db.Close)
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// staleNotFoundBarrierStore deterministically recreates the handler race where
// two requests both observe not-found, the winner commits, and the loser then
// continues from its stale result into a consumption check.
type staleNotFoundBarrierStore struct {
	store.OperationStore
	key            string
	mu             sync.Mutex
	resolveCount   int
	secondResolved chan struct{}
	committed      chan struct{}
	commitOnce     sync.Once
}

type invalidReceiptAcceptanceStore struct{ store.OperationStore }

func (s invalidReceiptAcceptanceStore) ResolveIdentity(ctx context.Context, identity store.OperationRequestIdentity) (store.AcceptedOperation, error) {
	return s.OperationStore.(store.OperationIdentityResolver).ResolveIdentity(ctx, identity)
}

func (s invalidReceiptAcceptanceStore) Accept(ctx context.Context, acceptance store.OperationAcceptance) (store.AcceptedOperation, error) {
	acceptance.Audit.RequestReceiptID = uuid.NewString()
	return s.OperationStore.Accept(ctx, acceptance)
}

func newStaleNotFoundBarrierStore(inner store.OperationStore, key string) *staleNotFoundBarrierStore {
	return &staleNotFoundBarrierStore{OperationStore: inner, key: key, secondResolved: make(chan struct{}), committed: make(chan struct{})}
}

func (s *staleNotFoundBarrierStore) ResolveIdentity(ctx context.Context, identity store.OperationRequestIdentity) (store.AcceptedOperation, error) {
	resolver := s.OperationStore.(store.OperationIdentityResolver)
	if identity.Key != s.key {
		return resolver.ResolveIdentity(ctx, identity)
	}
	s.mu.Lock()
	s.resolveCount++
	call := s.resolveCount
	s.mu.Unlock()
	if call > 2 {
		return resolver.ResolveIdentity(ctx, identity)
	}
	accepted, err := resolver.ResolveIdentity(ctx, identity)
	if !errors.Is(err, store.ErrAcceptanceNotFound) {
		return accepted, err
	}
	if call == 1 {
		select {
		case <-s.secondResolved:
		case <-ctx.Done():
			return store.AcceptedOperation{}, ctx.Err()
		}
		return accepted, err
	}
	close(s.secondResolved)
	select {
	case <-s.committed:
		return accepted, err
	case <-ctx.Done():
		return store.AcceptedOperation{}, ctx.Err()
	}
}

func (s *staleNotFoundBarrierStore) Accept(ctx context.Context, acceptance store.OperationAcceptance) (store.AcceptedOperation, error) {
	accepted, err := s.OperationStore.Accept(ctx, acceptance)
	if acceptance.Identity.Key == s.key && err == nil {
		s.commitOnce.Do(func() { close(s.committed) })
	}
	return accepted, err
}

func TestMaintenanceHTTPAcceptanceUsesReservedReceiptAndStableDeviceActor(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	key := "maintenance-" + suffix
	tokenOne := "token-one-" + suffix
	tokenTwo := "token-two-" + suffix
	deviceID := "device-" + suffix
	auditKey := strings.Repeat("a", 32)
	h := New(db, nil, nil, nil, &config.Config{AuditSigningKey: auditKey}, nil, nil, nil, nil, nil, nil)

	var operationID string
	t.Cleanup(func() {
		if operationID != "" {
			_, _ = db.Pool.Exec(context.Background(), `DELETE FROM operation_acceptance_intents WHERE operation_id=$1`, operationID)
			_, _ = db.Pool.Exec(context.Background(), `DELETE FROM operation_request_identities WHERE operation_id=$1`, operationID)
			_, _ = db.Pool.Exec(context.Background(), `DELETE FROM operations WHERE id=$1`, operationID)
		}
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM mutation_audit_events WHERE token_id IN ($1,$2)`, tokenOne, tokenTwo)
	})

	serve := func(tokenID, drainMode string) *httptest.ResponseRecorder {
		body := bytes.NewBufferString(`{"ref":"abc123","mode":"restart","drainMode":"` + drainMode + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/upgrades", body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		req = WithAccessPrincipal(req, &AccessPrincipal{
			Subject: "same display device", TokenID: tokenID, DeviceID: deviceID,
			Scopes: []string{ScopePlatformOperate}, Source: AccessPrincipalSourceManagedToken,
		})
		rec := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(h.QueuePlatformUpgrade)).ServeHTTP(rec, req)
		return rec
	}

	first := serve(tokenOne, "wait")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var firstOperation model.Operation
	if err := json.Unmarshal(first.Body.Bytes(), &firstOperation); err != nil {
		t.Fatal(err)
	}
	operationID = firstOperation.ID

	// A rotated credential for the same registered device must resolve the
	// original acceptance, not create a second operation.
	second := serve(tokenTwo, "wait")
	if second.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", second.Code, second.Body.String())
	}
	var replay model.Operation
	if err := json.Unmarshal(second.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.ID != operationID {
		t.Fatalf("replay operation=%q, want %q", replay.ID, operationID)
	}

	changed := serve(tokenTwo, "force")
	if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), "idempotency_key_reused") {
		t.Fatalf("changed request status=%d body=%s", changed.Code, changed.Body.String())
	}

	var identities, intents, operations int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_request_identities WHERE request_key=$1`, key).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_acceptance_intents WHERE operation_id=$1`, operationID).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE id=$1`, operationID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if identities != 1 || intents != 1 || operations != 1 {
		t.Fatalf("rows identity=%d intent=%d operation=%d", identities, intents, operations)
	}
	var receiptID, actorSubject, credentialID, canonical string
	if err := db.Pool.QueryRow(ctx, `
		SELECT i.request_receipt_id, r.actor_subject, i.credential_id, convert_from(i.canonical_bytes,'UTF8')
		FROM operation_acceptance_intents i
		JOIN operation_request_identities r ON r.id=i.request_identity_id
		WHERE i.operation_id=$1
	`, operationID).Scan(&receiptID, &actorSubject, &credentialID, &canonical); err != nil {
		t.Fatal(err)
	}
	if receiptID == "" || actorSubject != deviceID || credentialID != tokenOne || !strings.Contains(canonical, receiptID) {
		t.Fatalf("receipt=%q actor=%q credential=%q canonical=%s", receiptID, actorSubject, credentialID, canonical)
	}
}

func TestManagedTokenRotationPreservesActorAndCIRotationFailsClosed(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	secret := strings.Repeat("t", 32)
	cfg := &config.Config{APIToken: secret, AuditSigningKey: strings.Repeat("a", 32)}
	h := New(db, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil)
	now := time.Now().UTC()

	rootJTI := "managed-root-" + suffix
	rootClaims := tokenClaims{Sub: "shared display", Iss: "norn", Aud: "norn-control", Use: "access", Managed: true, Iat: now.Unix(), Exp: now.Add(time.Hour).Unix(), Jti: rootJTI, Scopes: []string{ScopePlatformOperate}}
	rootToken, err := signToken(secret, rootClaims)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RecordAccessToken(ctx, &store.AccessToken{JTI: rootJTI, Subject: rootClaims.Sub, Scopes: rootClaims.Scopes, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var rotatedJTI string
	ciJTI := "ci-token-" + suffix
	ciRefreshJTI := "ci-refresh-" + suffix
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM access_tokens WHERE jti IN ($1,$2,$3,$4)`, rootJTI, rotatedJTI, ciJTI, ciRefreshJTI)
	})

	rootPrincipal, ok := h.VerifyAccessToken(rootToken)
	if !ok {
		t.Fatal("root managed token did not verify")
	}
	rootActor, err := h.resolveVerifiedOperationActor(ctx, requestWithPrincipal(*rootPrincipal), "authority-1")
	if err != nil {
		t.Fatal(err)
	}
	rotateRequest := requestWithPrincipal(*rootPrincipal)
	rotateResponse := httptest.NewRecorder()
	h.RotateCurrentToken(rotateResponse, rotateRequest)
	if rotateResponse.Code != http.StatusOK {
		t.Fatalf("rotate status=%d body=%s", rotateResponse.Code, rotateResponse.Body.String())
	}
	var rotated struct {
		Token   string `json:"token"`
		TokenID string `json:"tokenId"`
	}
	if err := json.Unmarshal(rotateResponse.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	rotatedJTI = rotated.TokenID
	rotatedPrincipal, ok := h.VerifyAccessToken(rotated.Token)
	if !ok {
		t.Fatal("rotated managed token did not verify")
	}
	rotatedActor, err := h.resolveVerifiedOperationActor(ctx, requestWithPrincipal(*rotatedPrincipal), "authority-1")
	if err != nil {
		t.Fatal(err)
	}
	if rootActor.Issuer != rotatedActor.Issuer || rootActor.Subject != rotatedActor.Subject || rotatedActor.Subject != rootJTI {
		t.Fatalf("rotation changed actor: root=%+v rotated=%+v", rootActor, rotatedActor)
	}

	ci := &CIIdentity{Provider: "github-actions", RepositoryOwnerID: "owner-1", RepositoryID: "repo-1", RunID: "run-1", RunAttempt: "1"}
	issueCI := func(jti string) (*AccessPrincipal, verifiedOperationActor) {
		t.Helper()
		claims := tokenClaims{Sub: "github-actions:repo:run-1", Iss: "norn", Aud: "norn-control", Use: "access", Managed: true, Iat: now.Unix(), Exp: now.Add(time.Hour).Unix(), Jti: jti, Scopes: []string{ScopeReleaseStage}, CI: ci}
		token, err := signToken(secret, claims)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.RecordAccessToken(ctx, &store.AccessToken{JTI: jti, Subject: claims.Sub, Scopes: claims.Scopes, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		principal, ok := h.VerifyAccessToken(token)
		if !ok {
			t.Fatal("CI token did not verify")
		}
		actor, err := h.resolveVerifiedOperationActor(ctx, requestWithPrincipal(*principal), "authority-1")
		if err != nil {
			t.Fatal(err)
		}
		return principal, actor
	}
	ciPrincipal, ciActor := issueCI(ciJTI)
	ciRotate := httptest.NewRecorder()
	h.RotateCurrentToken(ciRotate, requestWithPrincipal(*ciPrincipal))
	if ciRotate.Code != http.StatusConflict || !strings.Contains(ciRotate.Body.String(), "github_actions_rotation_unsupported") {
		t.Fatalf("CI rotate status=%d body=%s", ciRotate.Code, ciRotate.Body.String())
	}
	_, refreshedActor := issueCI(ciRefreshJTI)
	if refreshedActor.Issuer != ciActor.Issuer || refreshedActor.Subject != ciActor.Subject {
		t.Fatalf("OIDC refresh changed actor: first=%+v refreshed=%+v", ciActor, refreshedActor)
	}
}

func TestRollbackHTTPSameKeyReplaysFrozenTargetAfterQueueChangesLatest(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, "demo")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte(`name: demo
deploy: true
processes:
  web:
    command: echo ok
regions:
  iad:
    nomadRegion: global
    datacenters: [dc1]
  ord:
    nomadRegion: global
    datacenters: [dc2]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	previous := model.Deployment{ID: uuid.NewString(), App: "demo", CommitSHA: "old", ImageTag: "registry/demo@sha256:" + strings.Repeat("a", 64), Environment: "staging", SagaID: uuid.NewString(), Status: model.StatusDeployed, StartedAt: now.Add(-2 * time.Hour)}
	current := model.Deployment{ID: uuid.NewString(), App: "demo", CommitSHA: "new", ImageTag: "registry/demo@sha256:" + strings.Repeat("b", 64), Environment: "staging", SagaID: uuid.NewString(), Status: model.StatusDeployed, StartedAt: now.Add(-time.Hour)}
	if err := db.InsertDeployment(context.Background(), &previous); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertDeployment(context.Background(), &current); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{AppsDir: appsDir, AuditSigningKey: strings.Repeat("r", 32)}
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool)}
	h := New(db, nil, nil, nil, cfg, pipe, nil, nil, pipe.SagaStore, nil, nil)
	pipe.SetOperationStore(h.OperationStore())
	serve := func(regions []string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]interface{}{"regions": regions})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/apps/demo/rollback", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "stable-rollback")
		route := chi.NewRouteContext()
		route.URLParams.Add("id", "demo")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "service", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIWrite}})
		rec := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(h.Rollback)).ServeHTTP(rec, req)
		return rec
	}

	first := serve([]string{"iad"})
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var firstBody map[string]interface{}
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}
	second := serve([]string{"iad"})
	if second.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", second.Code, second.Body.String())
	}
	var secondBody map[string]interface{}
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatal(err)
	}
	if firstBody["operationId"] != secondBody["operationId"] || firstBody["sagaId"] != secondBody["sagaId"] {
		t.Fatalf("replay changed accepted target: first=%v second=%v", firstBody, secondBody)
	}
	changed := serve([]string{"ord"})
	if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), "idempotency_key_reused") {
		t.Fatalf("changed regions status=%d body=%s", changed.Code, changed.Body.String())
	}
	var deploymentCount, operationCount int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM deployments WHERE app='demo'`).Scan(&deploymentCount); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE app='demo' AND kind='app.rollback'`).Scan(&operationCount); err != nil {
		t.Fatal(err)
	}
	if deploymentCount != 3 || operationCount != 1 {
		t.Fatalf("replay duplicated work: deployments=%d rollbackOperations=%d", deploymentCount, operationCount)
	}
}

func TestReleasePromotionHTTPSameKeyReplaysBeforeQualificationConsumption(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, "demo")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte(`name: demo
deploy: true
repo:
  url: https://example.test/owner/repo.git
processes:
  web:
    command: echo ok
`), 0o600); err != nil {
		t.Fatal(err)
	}
	qualificationKey := releaseTestKey('q')
	qualification := testQualification(t, qualificationKey)
	cfg := &config.Config{
		AppsDir:                         appsDir,
		Environment:                     "production",
		AuditSigningKey:                 strings.Repeat("p", 32),
		TrustedQualificationSigningKeys: []string{releaseTestPublicKey('q')},
	}
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool), Production: true}
	h := New(db, nil, nil, nil, cfg, pipe, nil, nil, pipe.SagaStore, nil, nil)
	pipe.SetOperationStore(h.OperationStore())
	body, err := json.Marshal(promotionRequest{SourceSHA: qualification.SourceSHA, Artifact: qualification.Artifact, Qualification: qualification})
	if err != nil {
		t.Fatal(err)
	}
	serve := func(deviceID, key string, requestBody []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/demo/promotions", bytes.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", "demo")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", TokenID: "token-" + deviceID, DeviceID: deviceID, Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAdmin}})
		rec := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(h.QueueReleasePromotion)).ServeHTTP(rec, req)
		return rec
	}

	first := serve("device-one", "promotion-replay", body)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var firstOperation model.Operation
	if err := json.Unmarshal(first.Body.Bytes(), &firstOperation); err != nil {
		t.Fatal(err)
	}
	replay := serve("device-one", "promotion-replay", body)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayOperation model.Operation
	if err := json.Unmarshal(replay.Body.Bytes(), &replayOperation); err != nil {
		t.Fatal(err)
	}
	if replayOperation.ID != firstOperation.ID {
		t.Fatalf("promotion replay operation=%q want=%q", replayOperation.ID, firstOperation.ID)
	}
	foreign := serve("device-two", "promotion-replay", body)
	if foreign.Code != http.StatusConflict || !strings.Contains(foreign.Body.String(), "qualification_already_consumed") {
		t.Fatalf("foreign actor status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	var count int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE kind='app.deploy' AND metadata->'promotionQualification'->>'id'=$1`, qualification.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("promotion replay created %d operations", count)
	}
	if _, err := db.Pool.Exec(context.Background(), `UPDATE operations SET status='succeeded', finished_at=now() WHERE id=$1`, firstOperation.ID); err != nil {
		t.Fatal(err)
	}

	// Exercise the not-found/accept race with a fresh qualification. Both
	// requests start together; the loser must resolve the committed winner
	// rather than report that the qualification was consumed.
	concurrentQualification := qualification
	concurrentQualification.ID = uuid.NewString()
	if err := signReleaseQualification(qualificationKey, &concurrentQualification); err != nil {
		t.Fatal(err)
	}
	concurrentBody, err := json.Marshal(promotionRequest{SourceSHA: concurrentQualification.SourceSHA, Artifact: concurrentQualification.Artifact, Qualification: concurrentQualification})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	baseOperationStore := h.OperationStore()
	pipe.SetOperationStore(newStaleNotFoundBarrierStore(baseOperationStore, "promotion-concurrent"))
	defer pipe.SetOperationStore(baseOperationStore)
	responses := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			responses <- serve("device-one", "promotion-concurrent", concurrentBody)
		}()
	}
	close(start)
	concurrentOperations := map[string]struct{}{}
	statuses := map[int]int{}
	for i := 0; i < 2; i++ {
		response := <-responses
		statuses[response.Code]++
		if response.Code != http.StatusAccepted && response.Code != http.StatusOK {
			t.Fatalf("concurrent promotion status=%d body=%s", response.Code, response.Body.String())
		}
		var operation model.Operation
		if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil {
			t.Fatal(err)
		}
		concurrentOperations[operation.ID] = struct{}{}
	}
	if len(concurrentOperations) != 1 || statuses[http.StatusAccepted] != 1 || statuses[http.StatusOK] != 1 {
		t.Fatalf("concurrent replay did not converge: operations=%v statuses=%v", concurrentOperations, statuses)
	}
}

func TestWebhookProviderDeliveryIdentityReplaysAndChangedBodyConflicts(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, "demo")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte(`name: demo
deploy: true
repo:
  url: https://example.test/owner/repo.git
  branch: main
  autoDeploy: true
processes:
  web:
    command: echo ok
`), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "webhook-test-secret"
	cfg := &config.Config{AppsDir: appsDir, WebhookSecret: secret, AuditSigningKey: strings.Repeat("w", 32)}
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool)}
	h := New(db, nil, nil, nil, cfg, pipe, nil, nil, pipe.SagaStore, nil, nil)
	pipe.SetOperationStore(h.OperationStore())
	serve := func(body string) *httptest.ResponseRecorder {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(body))
		req := httptest.NewRequest(http.MethodPost, "/api/webhooks/github", strings.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-GitHub-Delivery", "provider-delivery-1")
		route := chi.NewRouteContext()
		route.URLParams.Add("provider", "github")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		rec := httptest.NewRecorder()
		h.Webhook(rec, req)
		return rec
	}
	body := `{"ref":"refs/heads/main","repository":{"clone_url":"https://example.test/owner/repo.git"}}`
	first := serve(body)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var firstBody map[string]string
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}
	replay := serve(body)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayBody map[string]string
	if err := json.Unmarshal(replay.Body.Bytes(), &replayBody); err != nil {
		t.Fatal(err)
	}
	if replayBody["operationId"] != firstBody["operationId"] || replayBody["sagaId"] != firstBody["sagaId"] {
		t.Fatalf("webhook replay changed operation: first=%v replay=%v", firstBody, replayBody)
	}
	changed := serve(`{"ref":"refs/heads/main","after":"different","repository":{"clone_url":"https://example.test/owner/repo.git"}}`)
	if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), "idempotency_key_reused") {
		t.Fatalf("changed body status=%d body=%s", changed.Code, changed.Body.String())
	}
	var operations int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE app='demo' AND kind='app.deploy'`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 1 {
		t.Fatalf("webhook retry created %d operations", operations)
	}
}

func TestReleaseQualificationHTTPUsesSignedAtomicAcceptance(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	qualificationKey := releaseTestKey('s')
	cfg := &config.Config{Environment: "staging", AuditSigningKey: strings.Repeat("q", 32), QualificationSigningKey: qualificationKey}
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool)}
	h := New(db, nil, nil, nil, cfg, pipe, nil, nil, pipe.SagaStore, nil, nil)
	pipe.SetOperationStore(h.OperationStore())
	candidate := testReleaseCandidate()
	now := time.Now().UTC()
	insertRelease := func(started time.Time) model.Deployment {
		deployment := model.Deployment{ID: uuid.NewString(), App: "demo", CommitSHA: strings.Repeat("a", 40), ImageTag: "registry.example.test/demo@sha256:" + strings.Repeat("b", 64), Environment: "staging", SagaID: uuid.NewString(), Status: model.StatusDeployed, StartedAt: started}
		if err := db.InsertDeployment(context.Background(), &deployment); err != nil {
			t.Fatal(err)
		}
		finished := started
		op := model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: "demo", Ref: deployment.CommitSHA, Status: model.OperationSucceeded, Source: "release-control-api", StartedAt: started, FinishedAt: &finished, MaxAttempts: 1, Payload: map[string]interface{}{"deploymentId": deployment.ID, "sourceSha": deployment.CommitSHA, "artifact": deployment.ImageTag, "candidate": candidate}, Metadata: map[string]interface{}{"candidate": candidate}}
		if err := db.InsertCompletedOperation(context.Background(), &op); err != nil {
			t.Fatal(err)
		}
		return deployment
	}
	firstDeployment := insertRelease(now.Add(-2 * time.Minute))
	secondDeployment := insertRelease(now.Add(-time.Minute))
	thirdDeployment := insertRelease(now)

	servePrincipal := func(principal AccessPrincipal, key, deploymentID string) *httptest.ResponseRecorder {
		body, err := json.Marshal(qualificationRequest{DeploymentID: deploymentID})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/demo/release-qualifications", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", "demo")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		req = WithAccessPrincipal(req, &principal)
		rec := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(h.CreateReleaseQualification)).ServeHTTP(rec, req)
		return rec
	}
	serve := func(deviceID, key, deploymentID string) *httptest.ResponseRecorder {
		return servePrincipal(AccessPrincipal{Subject: "operator", TokenID: "token-" + deviceID, DeviceID: deviceID, Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAdmin}}, key, deploymentID)
	}

	first := serve("device-one", "qualification-replay", firstDeployment.ID)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var firstReceipt model.ReleaseQualification
	if err := json.Unmarshal(first.Body.Bytes(), &firstReceipt); err != nil {
		t.Fatal(err)
	}
	immediate := serve("device-one", "qualification-replay", firstDeployment.ID)
	if immediate.Code != http.StatusOK {
		t.Fatalf("immediate replay status=%d body=%s", immediate.Code, immediate.Body.String())
	}
	var immediateReceipt model.ReleaseQualification
	if err := json.Unmarshal(immediate.Body.Bytes(), &immediateReceipt); err != nil {
		t.Fatal(err)
	}
	if immediateReceipt.ID != firstReceipt.ID || immediateReceipt.Signature != firstReceipt.Signature {
		t.Fatalf("replay regenerated qualification: first=%+v replay=%+v", firstReceipt, immediateReceipt)
	}
	changed := serve("device-one", "qualification-replay", secondDeployment.ID)
	if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), "idempotency_key_reused") {
		t.Fatalf("changed deployment status=%d body=%s", changed.Code, changed.Body.String())
	}
	foreign := serve("device-two", "qualification-replay", firstDeployment.ID)
	if foreign.Code != http.StatusCreated {
		t.Fatalf("foreign actor status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	var foreignReceipt model.ReleaseQualification
	if err := json.Unmarshal(foreign.Body.Bytes(), &foreignReceipt); err != nil {
		t.Fatal(err)
	}
	if foreignReceipt.ID == firstReceipt.ID {
		t.Fatal("different actor replayed another actor's qualification")
	}

	if _, err := db.Pool.Exec(context.Background(), `UPDATE deployments SET status='failed' WHERE id=$1`, firstDeployment.ID); err != nil {
		t.Fatal(err)
	}
	afterAdvance := serve("device-one", "qualification-replay", firstDeployment.ID)
	if afterAdvance.Code != http.StatusOK {
		t.Fatalf("replay after deployment state advance status=%d body=%s", afterAdvance.Code, afterAdvance.Body.String())
	}
	var advancedReceipt model.ReleaseQualification
	if err := json.Unmarshal(afterAdvance.Body.Bytes(), &advancedReceipt); err != nil {
		t.Fatal(err)
	}
	if advancedReceipt.ID != firstReceipt.ID {
		t.Fatalf("state advance changed replay receipt=%q want=%q", advancedReceipt.ID, firstReceipt.ID)
	}

	ci := &CIIdentity{Provider: candidate.Provider, Repository: candidate.Repository, RepositoryID: candidate.RepositoryID, RepositoryOwnerID: candidate.OwnerID, RunID: candidate.RunID, RunAttempt: candidate.RunAttempt, WorkflowRef: candidate.WorkflowRef, WorkflowSHA: candidate.WorkflowSHA, JobWorkflowRef: candidate.SignerWorkflowRef, JobWorkflowSHA: candidate.SignerWorkflowSHA, Ref: candidate.Ref, SHA: candidate.Attestation.MaterialSHA, Intent: "qualify"}
	ciPrincipal := AccessPrincipal{Subject: "workflow", TokenID: "ci-token-one", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeReleaseQualify}, App: "demo", Environment: "staging", CI: ci}
	ciFirst := servePrincipal(ciPrincipal, "qualification-current-auth", thirdDeployment.ID)
	if ciFirst.Code != http.StatusCreated {
		t.Fatalf("CI qualification status=%d body=%s", ciFirst.Code, ciFirst.Body.String())
	}
	changedCI := *ci
	changedCI.Intent = "deploy"
	ciPrincipal.TokenID = "ci-token-refreshed"
	ciPrincipal.CI = &changedCI
	ciReplay := servePrincipal(ciPrincipal, "qualification-current-auth", thirdDeployment.ID)
	if ciReplay.Code != http.StatusForbidden || !strings.Contains(ciReplay.Body.String(), "qualification_intent_invalid") {
		t.Fatalf("CI replay bypassed current intent policy status=%d body=%s", ciReplay.Code, ciReplay.Body.String())
	}

	start := make(chan struct{})
	qualificationBaseStore := h.OperationStore()
	pipe.SetOperationStore(newStaleNotFoundBarrierStore(qualificationBaseStore, "qualification-concurrent"))
	defer pipe.SetOperationStore(qualificationBaseStore)
	responses := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			responses <- serve("device-one", "qualification-concurrent", secondDeployment.ID)
		}()
	}
	close(start)
	concurrentIDs := map[string]struct{}{}
	statuses := map[int]int{}
	for i := 0; i < 2; i++ {
		response := <-responses
		statuses[response.Code]++
		if response.Code != http.StatusCreated && response.Code != http.StatusOK {
			t.Fatalf("concurrent qualification status=%d body=%s", response.Code, response.Body.String())
		}
		var receipt model.ReleaseQualification
		if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		concurrentIDs[receipt.ID] = struct{}{}
	}
	if len(concurrentIDs) != 1 || statuses[http.StatusCreated] != 1 || statuses[http.StatusOK] != 1 {
		t.Fatalf("concurrent qualification did not converge: ids=%v statuses=%v", concurrentIDs, statuses)
	}

	var acceptedRows, legacyMetadata int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations o JOIN operation_acceptance_intents i ON i.operation_id=o.id WHERE o.kind='release.qualification'`).Scan(&acceptedRows); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE kind='release.qualification' AND (metadata ? 'idempotencyKey' OR metadata ? 'requestDigest')`).Scan(&legacyMetadata); err != nil {
		t.Fatal(err)
	}
	if acceptedRows != 4 || legacyMetadata != 0 {
		t.Fatalf("qualification acceptance rows=%d legacyMetadata=%d", acceptedRows, legacyMetadata)
	}
}

func TestFleetCapacityPlanHTTPUsesSignedAcceptanceBeforeInventory(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "fleet.yaml")
	document := `apiVersion: norn.dev/fleet/v1
kind: Cluster
cluster: {name: staging-nyc3, provider: digitalocean, region: nyc3}
nodePools:
  app:
    size: s-4vcpu-8gb
    min: 2
    desired: 2
    max: 8
    replacement: {strategy: blueGreen, requireCapacityHeadroom: true, requireReadiness: true}
`
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{FleetConfig: configPath, AuditSigningKey: strings.Repeat("f", 32)}
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool)}
	h := New(db, nil, nil, nil, cfg, pipe, nil, nil, pipe.SagaStore, nil, nil)
	baseOperationStore := h.OperationStore()
	pipe.SetOperationStore(baseOperationStore)
	serve := func(deviceID, key string, request fleet.PlanRequest) *httptest.ResponseRecorder {
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/node-pools/app/plan", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		route := chi.NewRouteContext()
		route.URLParams.Add("pool", "app")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", TokenID: "token-" + deviceID, DeviceID: deviceID, Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAdmin}})
		rec := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(h.PlanFleetCapacity)).ServeHTTP(rec, req)
		return rec
	}
	desiredThree := 3
	request := fleet.PlanRequest{Desired: &desiredThree, Reason: "  add headroom  "}
	first := serve("device-one", "fleet-plan-replay", request)
	if first.Code != http.StatusCreated || first.Header().Get("Location") == "" {
		t.Fatalf("first status=%d location=%q body=%s", first.Code, first.Header().Get("Location"), first.Body.String())
	}
	var firstOperation model.Operation
	if err := json.Unmarshal(first.Body.Bytes(), &firstOperation); err != nil {
		t.Fatal(err)
	}
	foreign := serve("device-two", "fleet-plan-replay", fleet.PlanRequest{Desired: &desiredThree, Reason: "add headroom"})
	if foreign.Code != http.StatusCreated {
		t.Fatalf("foreign actor status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	var foreignOperation model.Operation
	if err := json.Unmarshal(foreign.Body.Bytes(), &foreignOperation); err != nil {
		t.Fatal(err)
	}
	if foreignOperation.ID == firstOperation.ID {
		t.Fatal("different actor replayed another actor's fleet plan")
	}
	desiredFour := 4
	changed := serve("device-one", "fleet-plan-replay", fleet.PlanRequest{Desired: &desiredFour, Reason: "add headroom"})
	if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), "idempotency_key_reused") {
		t.Fatalf("changed request status=%d body=%s", changed.Code, changed.Body.String())
	}
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	replay := serve("device-one", "fleet-plan-replay", fleet.PlanRequest{Desired: &desiredThree, Reason: "add headroom"})
	if replay.Code != http.StatusOK {
		t.Fatalf("replay without inventory status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayOperation model.Operation
	if err := json.Unmarshal(replay.Body.Bytes(), &replayOperation); err != nil {
		t.Fatal(err)
	}
	if replayOperation.ID != firstOperation.ID || replayOperation.Receipt == nil {
		t.Fatalf("inventory-free replay changed result: first=%q replay=%q receipt=%#v", firstOperation.ID, replayOperation.ID, replayOperation.Receipt)
	}
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	barrier := newStaleNotFoundBarrierStore(baseOperationStore, "fleet-plan-concurrent")
	pipe.SetOperationStore(barrier)
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			responses <- serve("device-one", "fleet-plan-concurrent", fleet.PlanRequest{Desired: &desiredFour, Reason: "grow"})
		}()
	}
	close(start)
	concurrentIDs := map[string]struct{}{}
	statuses := map[int]int{}
	for i := 0; i < 2; i++ {
		response := <-responses
		statuses[response.Code]++
		if response.Code != http.StatusCreated && response.Code != http.StatusOK {
			t.Fatalf("concurrent fleet plan status=%d body=%s", response.Code, response.Body.String())
		}
		var operation model.Operation
		if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil {
			t.Fatal(err)
		}
		concurrentIDs[operation.ID] = struct{}{}
	}
	if len(concurrentIDs) != 1 || statuses[http.StatusCreated] != 1 || statuses[http.StatusOK] != 1 {
		t.Fatalf("concurrent fleet plan did not converge: ids=%v statuses=%v", concurrentIDs, statuses)
	}

	pipe.SetOperationStore(invalidReceiptAcceptanceStore{OperationStore: baseOperationStore})
	var operationsBefore int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE kind='fleet.capacity-plan'`).Scan(&operationsBefore); err != nil {
		t.Fatal(err)
	}
	failed := serve("device-one", "fleet-plan-rollback", fleet.PlanRequest{Desired: &desiredFour, Reason: "failure injection"})
	if failed.Code < 500 {
		t.Fatalf("forced acceptance failure status=%d body=%s", failed.Code, failed.Body.String())
	}
	var operationsAfter, identitiesAfter int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE kind='fleet.capacity-plan'`).Scan(&operationsAfter); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operation_request_identities WHERE request_key='fleet-plan-rollback'`).Scan(&identitiesAfter); err != nil {
		t.Fatal(err)
	}
	if operationsAfter != operationsBefore || identitiesAfter != 0 {
		t.Fatalf("failed acceptance leaked rows: operations before=%d after=%d identities=%d", operationsBefore, operationsAfter, identitiesAfter)
	}
	pipe.SetOperationStore(baseOperationStore)
}

func TestFleetReconciliationHTTPUsesTypedAtomicAcceptance(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "fleet.yaml")
	document := `apiVersion: norn.dev/fleet/v1
kind: Cluster
cluster: {name: staging-nyc3, provider: digitalocean, region: nyc3}
nodePools:
  app:
    size: s-4vcpu-8gb
    min: 2
    desired: 2
    max: 8
    replacement: {strategy: blueGreen, requireCapacityHeadroom: true, requireReadiness: true}
`
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{FleetConfig: configPath, AuditSigningKey: strings.Repeat("e", 32)}
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool)}
	h := New(db, nil, nil, nil, cfg, pipe, nil, nil, pipe.SagaStore, nil, nil)
	pipe.SetOperationStore(h.OperationStore())

	desired := 3
	planBody, _ := json.Marshal(fleet.PlanRequest{Desired: &desired, Reason: "reconciliation acceptance fixture"})
	planRequest := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/node-pools/app/plan", bytes.NewReader(planBody))
	planRequest.Header.Set("Content-Type", "application/json")
	planRequest.Header.Set("Idempotency-Key", "reconciliation-plan")
	planRoute := chi.NewRouteContext()
	planRoute.URLParams.Add("pool", "app")
	planRequest = planRequest.WithContext(context.WithValue(planRequest.Context(), chi.RouteCtxKey, planRoute))
	planRequest = WithAccessPrincipal(planRequest, &AccessPrincipal{Subject: "operator", TokenID: "token-device-one", DeviceID: "device-one", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAdmin}})
	planResponse := httptest.NewRecorder()
	h.MutationAuditMiddleware(http.HandlerFunc(h.PlanFleetCapacity)).ServeHTTP(planResponse, planRequest)
	if planResponse.Code != http.StatusCreated {
		t.Fatalf("capacity plan status=%d body=%s", planResponse.Code, planResponse.Body.String())
	}
	var planOperation model.Operation
	if err := json.Unmarshal(planResponse.Body.Bytes(), &planOperation); err != nil {
		t.Fatal(err)
	}

	serve := func(deviceID, key string, request fleet.ReconciliationRequest) *httptest.ResponseRecorder {
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+planOperation.ID+"/reconciliations", bytes.NewReader(body))
		httpRequest.Header.Set("Content-Type", "application/json")
		httpRequest.Header.Set("Idempotency-Key", key)
		route := chi.NewRouteContext()
		route.URLParams.Add("planID", planOperation.ID)
		httpRequest = httpRequest.WithContext(context.WithValue(httpRequest.Context(), chi.RouteCtxKey, route))
		httpRequest = WithAccessPrincipal(httpRequest, &AccessPrincipal{Subject: "operator", TokenID: "token-" + deviceID, DeviceID: deviceID, Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAdmin}})
		response := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(h.RecordFleetReconciliation)).ServeHTTP(response, httpRequest)
		return response
	}
	request := fleet.ReconciliationRequest{
		SchemaVersion: fleet.ReconciliationSchemaVersion, Phase: "infrastructure_applied", Status: "succeeded",
		CommitSHA: strings.Repeat("a", 40), PlanSHA256: strings.Repeat("b", 64), StateSerial: 1,
		EvidenceDigest: "sha256:" + strings.Repeat("c", 64), Message: "provider apply complete",
	}
	first := serve("device-one", "reconciliation-replay", request)
	if first.Code != http.StatusCreated || first.Header().Get("Location") == "" {
		t.Fatalf("first reconciliation status=%d location=%q body=%s", first.Code, first.Header().Get("Location"), first.Body.String())
	}
	var firstOperation model.Operation
	if err := json.Unmarshal(first.Body.Bytes(), &firstOperation); err != nil {
		t.Fatal(err)
	}

	advanceRequest := request
	advanceRequest.Phase = "inventory_generated"
	advanceRequest.EvidenceDigest = "sha256:" + strings.Repeat("d", 64)
	advanced := serve("device-one", "reconciliation-advance", advanceRequest)
	if advanced.Code != http.StatusCreated {
		t.Fatalf("advanced reconciliation status=%d body=%s", advanced.Code, advanced.Body.String())
	}
	replay := serve("device-one", "reconciliation-replay", request)
	if replay.Code != http.StatusOK {
		t.Fatalf("reconciliation replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayOperation model.Operation
	if err := json.Unmarshal(replay.Body.Bytes(), &replayOperation); err != nil {
		t.Fatal(err)
	}
	if replayOperation.ID != firstOperation.ID || replayOperation.Receipt == nil {
		t.Fatalf("replay changed accepted reconciliation: first=%q replay=%q receipt=%#v", firstOperation.ID, replayOperation.ID, replayOperation.Receipt)
	}
	changedRequest := request
	changedRequest.EvidenceDigest = "sha256:" + strings.Repeat("f", 64)
	changed := serve("device-one", "reconciliation-replay", changedRequest)
	if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), "idempotency_key_reused") {
		t.Fatalf("changed reconciliation status=%d body=%s", changed.Code, changed.Body.String())
	}
	foreign := serve("device-two", "reconciliation-replay", request)
	if foreign.Code != http.StatusCreated {
		t.Fatalf("actor-scoped reconciliation status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	var acceptedRows, legacyMetadata int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations o JOIN operation_acceptance_intents i ON i.operation_id=o.id WHERE o.kind='fleet.reconciliation'`).Scan(&acceptedRows); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE kind='fleet.reconciliation' AND (metadata ? 'idempotencyKey' OR metadata ? 'requestDigest')`).Scan(&legacyMetadata); err != nil {
		t.Fatal(err)
	}
	if acceptedRows != 3 || legacyMetadata != 0 {
		t.Fatalf("reconciliation acceptance rows=%d legacyMetadata=%d", acceptedRows, legacyMetadata)
	}
}

func TestFleetRunnerAttemptHTTPUsesCompoundSignedAcceptance(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "fleet.yaml")
	document := `apiVersion: norn.dev/fleet/v1
kind: Cluster
metadata: {environment: staging}
cluster: {name: staging-nyc3, provider: digitalocean, region: nyc3}
nodePools:
  app:
    size: s-4vcpu-8gb
    min: 2
    desired: 2
    max: 8
    replacement: {strategy: blueGreen, requireCapacityHeadroom: true, requireReadiness: true}
`
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{FleetConfig: configPath, AuditSigningKey: strings.Repeat("d", 32)}
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool)}
	h := New(db, nil, nil, nil, cfg, pipe, nil, nil, pipe.SagaStore, nil, nil)
	pipe.SetOperationStore(h.OperationStore())

	desired := 3
	planBody, _ := json.Marshal(fleet.PlanRequest{Desired: &desired, Reason: "runner acceptance fixture"})
	planRequest := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/node-pools/app/plan", bytes.NewReader(planBody))
	planRequest.Header.Set("Content-Type", "application/json")
	planRequest.Header.Set("Idempotency-Key", "runner-plan")
	planRoute := chi.NewRouteContext()
	planRoute.URLParams.Add("pool", "app")
	planRequest = planRequest.WithContext(context.WithValue(planRequest.Context(), chi.RouteCtxKey, planRoute))
	planRequest = WithAccessPrincipal(planRequest, &AccessPrincipal{Subject: "operator", TokenID: "token-device", DeviceID: "device-one", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAdmin}})
	planResponse := httptest.NewRecorder()
	h.MutationAuditMiddleware(http.HandlerFunc(h.PlanFleetCapacity)).ServeHTTP(planResponse, planRequest)
	if planResponse.Code != http.StatusCreated {
		t.Fatalf("capacity plan status=%d body=%s", planResponse.Code, planResponse.Body.String())
	}
	var plan model.Operation
	if err := json.Unmarshal(planResponse.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	rawNonce := strings.Repeat("f", 64)
	nonceDigest := sha256.Sum256([]byte(rawNonce))
	nonceHash := hex.EncodeToString(nonceDigest[:])
	if _, err := db.CreateFleetGitHubDispatch(context.Background(), store.FleetGitHubDispatch{
		PlanID: plan.ID, PlanRunID: 6, PlanSHA256: strings.Repeat("b", 64), ApprovedHeadSHA: strings.Repeat("c", 40),
		FleetEnvironment: "staging/nyc3", DispatchNonce: rawNonce, DispatchNonceSHA256: nonceHash,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishFleetGitHubDispatch(context.Background(), plan.ID, nonceHash, 7, "https://github.com/acme/fleet/actions/runs/7"); err != nil {
		t.Fatal(err)
	}
	ci := &CIIdentity{
		Provider: "github-actions", Repository: "acme/fleet", RepositoryID: "2", RepositoryOwnerID: "1",
		RunID: "7", RunAttempt: "1", SHA: strings.Repeat("c", 40), Intent: "apply",
	}
	serveRunner := func(key string, timeout int, runnerAttemptID string) *httptest.ResponseRecorder {
		request := fleet.RunnerAttemptCreateRequest{
			SchemaVersion: fleet.RunnerAttemptSchemaVersion, RunnerAttemptID: runnerAttemptID,
			CommitSHA: ci.SHA, PlanSHA256: strings.Repeat("b", 64), WorkflowURL: canonicalWorkflowRunURL(ci.Repository, ci.RunID),
			DispatchNonce: rawNonce, SourceDispatchRunID: "7", HeartbeatTimeoutSeconds: timeout,
		}
		body, _ := json.Marshal(request)
		httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+plan.ID+"/attempts", bytes.NewReader(body))
		httpRequest.Header.Set("Content-Type", "application/json")
		if key != "" {
			httpRequest.Header.Set("Idempotency-Key", key)
		}
		route := chi.NewRouteContext()
		route.URLParams.Add("planID", plan.ID)
		httpRequest = httpRequest.WithContext(context.WithValue(httpRequest.Context(), chi.RouteCtxKey, route))
		httpRequest = WithAccessPrincipal(httpRequest, &AccessPrincipal{Subject: "runner", TokenID: "ci-token", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeFleetOperate}, CI: ci})
		response := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(h.CreateFleetRunnerAttempt)).ServeHTTP(response, httpRequest)
		return response
	}
	serve := func(key string, timeout int) *httptest.ResponseRecorder {
		return serveRunner(key, timeout, canonicalRunnerAttemptID(ci))
	}
	wrongRun := serveRunner("runner-attempt-wrong-run", 120, "github-actions:acme/fleet:8:1")
	if wrongRun.Code != http.StatusForbidden || !strings.Contains(wrongRun.Body.String(), "fleet_runner_attempt_identity_mismatch") {
		t.Fatalf("wrong runner identity status=%d body=%s", wrongRun.Code, wrongRun.Body.String())
	}
	missingKey := serve("", 120)
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("missing runner key status=%d body=%s", missingKey.Code, missingKey.Body.String())
	}
	first := serve("runner-attempt-replay", 120)
	if first.Code != http.StatusCreated || first.Header().Get("Location") == "" || strings.Contains(first.Body.String(), rawNonce) {
		t.Fatalf("first runner status=%d location=%q body=%s", first.Code, first.Header().Get("Location"), first.Body.String())
	}
	var firstAttempt fleet.RunnerAttempt
	if err := json.Unmarshal(first.Body.Bytes(), &firstAttempt); err != nil {
		t.Fatal(err)
	}
	replay := serve("runner-attempt-replay", 120)
	if replay.Code != http.StatusOK {
		t.Fatalf("runner replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayAttempt fleet.RunnerAttempt
	if err := json.Unmarshal(replay.Body.Bytes(), &replayAttempt); err != nil {
		t.Fatal(err)
	}
	if replayAttempt.ID != firstAttempt.ID {
		t.Fatalf("runner replay changed ID first=%q replay=%q", firstAttempt.ID, replayAttempt.ID)
	}
	changed := serve("runner-attempt-replay", 121)
	if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), "idempotency_key_reused") {
		t.Fatalf("changed runner timeout status=%d body=%s", changed.Code, changed.Body.String())
	}
	var acceptedRows, rawNonceRows int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations o JOIN operation_acceptance_intents i ON i.operation_id=o.id WHERE o.kind='fleet.runner-attempt'`).Scan(&acceptedRows); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE kind='fleet.runner-attempt' AND (payload ? 'dispatchNonce' OR payload::text LIKE '%' || $1 || '%')`, rawNonce).Scan(&rawNonceRows); err != nil {
		t.Fatal(err)
	}
	if acceptedRows != 1 || rawNonceRows != 0 {
		t.Fatalf("runner acceptance rows=%d rawNonceRows=%d", acceptedRows, rawNonceRows)
	}
}
