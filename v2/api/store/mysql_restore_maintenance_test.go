package store

import (
	"encoding/json"
	"testing"

	"norn/v2/api/database"
)

func TestMySQLRestoreSignedPayloadBindsMaintenanceIdentity(t *testing.T) {
	request := MySQLRestoreRequest{
		CatalogRevision: 12,
		Target:          database.TargetIdentity{ServiceID: "mysql", ServiceGeneration: 2, BindingID: "wordpress", BindingGeneration: 4, Engine: database.EngineMySQL, Database: "wordpress", Role: "wp"},
		Maintenance: database.MySQLMaintenanceCredentials{
			Generation: 7, RuntimeAccountHost: "%", RestoreRole: "wp_restore", RestoreCredentialRef: "secret:apps/wp-restore",
			FenceRole: "wp_fence", FenceCredentialRef: "secret:apps/wp-fence", FenceAccountHost: "%",
		},
	}
	if !sameMySQLRestorePayload(payloadForMySQLRestoreTest(t, request), request) {
		t.Fatal("canonical signed payload did not preserve the maintenance identity")
	}
	tampered := request
	tampered.Maintenance.RestoreCredentialRef = "secret:apps/wp-restore-rotated"
	if sameMySQLRestorePayload(payloadForMySQLRestoreTest(t, request), tampered) {
		t.Fatal("restore credential reference changed without invalidating the signed payload")
	}
	tampered = request
	tampered.Maintenance.FenceAccountHost = "localhost"
	if sameMySQLRestorePayload(payloadForMySQLRestoreTest(t, request), tampered) {
		t.Fatal("fence account host changed without invalidating the signed payload")
	}
}

func payloadForMySQLRestoreTest(t *testing.T, request MySQLRestoreRequest) map[string]interface{} {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}
