package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/database"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

func TestEtcdFleetDatabaseCatalogInspection(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-test/fleet-catalog/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("fleet-catalog-inspection-test-key-000")
	if err != nil {
		t.Fatal(err)
	}
	operations, err := etcdstore.NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	inspect := etcdFleetDatabaseCatalog(operations)
	call := func(scopes ...string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/database/catalog", nil)
		request = handler.WithAccessPrincipal(request, &handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Scopes: scopes})
		response := httptest.NewRecorder()
		inspect(response, request)
		return response
	}
	if response := call(handler.ScopeAPIRead); response.Code != http.StatusNotFound {
		t.Fatalf("missing catalog: %d %s", response.Code, response.Body)
	}
	catalog := database.Catalog{APIVersion: database.APIVersion,
		Services: []database.DatabaseService{{APIVersion: database.APIVersion, ID: "pg", Generation: 1, Purpose: database.PurposeApplication,
			Engine: database.EnginePostgreSQL, EngineVersion: "16", ProviderRef: "local:pg", Endpoint: database.DatabaseEndpoint{Host: "127.0.0.1", Port: 5432},
			Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled}, Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilityRuntime}}}},
		Bindings: []database.DatabaseBinding{{APIVersion: database.APIVersion, ID: "primary", ServiceID: "pg", Database: "app", Role: "app", Generation: 1,
			CredentialRef: "secret:private/catalog-ref", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}}}}
	if _, err := operations.ActivatePostgresDatabaseCatalog(context.Background(), 0, catalog, "test"); err != nil {
		t.Fatal(err)
	}
	if response := call(); response.Code != http.StatusForbidden {
		t.Fatalf("scope: %d", response.Code)
	}
	response := call(handler.ScopeAPIRead)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"revision":1`) || strings.Contains(response.Body.String(), "secret:private/catalog-ref") {
		t.Fatalf("inspection: %d %s", response.Code, response.Body)
	}
}
