package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/config"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

func TestAppScaleStatusSeparatesRegionalIntentFromNomadObservation(t *testing.T) {
	db := privateControlDB(t)
	root := t.TempDir()
	appDir := filepath.Join(root, "widget")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := []byte("name: widget\ndeploy: true\nregions:\n  ord:\n    nomadRegion: us-central\n  iad:\n    nomadRegion: us-east\nprocesses:\n  web:\n    command: run\n    scaling:\n      min: 1\n      per_region: 2\n  digest:\n    schedule: '0 8 * * *'\n    command: digest\n")
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), spec, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertCompletedOperation(t.Context(), &model.Operation{ID: "test-scale", Kind: "app.scale", App: "widget",
		SagaID: "test-saga", Status: model.OperationSucceeded, Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO app_desired_replicas(app,process,region,desired_count,revision,operation_id)
		VALUES('widget','web','ord',3,1,'test-scale')`); err != nil {
		t.Fatal(err)
	}
	nomadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/job/widget/scale" {
			http.NotFound(w, r)
			return
		}
		region := r.URL.Query().Get("region")
		count := 2
		if region == "us-central" {
			count = 3
		} else if region != "us-east" {
			http.Error(w, "unknown region", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(nomadapi.JobScaleStatusResponse{TaskGroups: map[string]nomadapi.TaskGroupScaleStatus{
			"web": {Desired: count, Placed: count - 1, Running: count - 1, Healthy: count - 1},
		}})
	}))
	defer nomadServer.Close()
	client, err := nomad.NewClient(nomadServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db, nomad: client, cfg: &config.Config{AppsDir: root}}
	req := withAppID(httptest.NewRequest(http.MethodGet, "/api/v1/apps/widget/scale-status", nil), "widget")
	rec := httptest.NewRecorder()
	h.GetAppScaleStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scale status HTTP %d: %s", rec.Code, rec.Body.String())
	}
	var rows []model.ProcessScaleStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Region != "iad" || rows[0].Desired != 2 || rows[0].IntentSource != "declared" ||
		rows[1].Region != "ord" || rows[1].Desired != 3 || rows[1].IntentSource != "accepted-scale" ||
		rows[1].NomadDesired == nil || *rows[1].NomadDesired != 3 || rows[1].Running == nil || *rows[1].Running != 2 {
		t.Fatalf("regional scale rows = %+v", rows)
	}
	nomadServer.Close()
	unavailable := httptest.NewRecorder()
	h.GetAppScaleStatus(unavailable, req)
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable Nomad returned HTTP %d, want 503 rather than a zero-count projection", unavailable.Code)
	}
}
