package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"norn/v2/api/pipeline"
)

func TestSnapshotExecutionUnavailableProblem(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/apps/demo/snapshots", nil)
	writeOperationAcceptanceError(recorder, request, &pipeline.SnapshotExecutionUnavailableError{})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", recorder.Code)
	}
	var problem Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "snapshot_execution_unavailable" {
		t.Fatalf("code=%q", problem.Code)
	}
}
