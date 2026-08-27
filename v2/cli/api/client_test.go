package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeviceEnrollmentAdministrationRoutes(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
			t.Fatalf("Authorization = %q, want bearer admin token", got)
		}
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/enrollments":
			_, _ = w.Write([]byte(`{"enrollments":[{"id":"enroll-1","deviceName":"Studio Mac","requestedScopes":["api:read","events:read"],"status":"pending","createdAt":"2026-08-26T20:00:00Z","expiresAt":"2026-08-26T20:10:00Z"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/enrollments/approve":
			var body struct {
				UserCode string   `json:"userCode"`
				Scopes   []string `json:"scopes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode approval: %v", err)
			}
			if body.UserCode != "ABCD-EFGH" || len(body.Scopes) != 2 {
				t.Fatalf("unexpected approval body: %+v", body)
			}
			_, _ = w.Write([]byte(`{"id":"enroll-1","deviceName":"Studio Mac","requestedScopes":["api:read","events:read"],"approvedScopes":["api:read","events:read"],"status":"approved","deviceId":"device-1","createdAt":"2026-08-26T20:00:00Z","expiresAt":"2026-08-26T20:10:00Z"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/devices":
			_, _ = w.Write([]byte(`{"devices":[{"id":"device-1","name":"Studio Mac","platform":"macOS","createdAt":"2026-08-26T20:01:00Z","tokens":[]}]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/devices/device-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL)
	client.Token = "admin-token"

	enrollments, err := client.ListDeviceEnrollments("pending")
	if err != nil || len(enrollments) != 1 || enrollments[0].DeviceName != "Studio Mac" {
		t.Fatalf("ListDeviceEnrollments() = %+v, %v", enrollments, err)
	}
	approved, err := client.ApproveDeviceEnrollment(" ABCD-EFGH ", []string{"api:read", "events:read"})
	if err != nil || approved.DeviceID != "device-1" {
		t.Fatalf("ApproveDeviceEnrollment() = %+v, %v", approved, err)
	}
	devices, err := client.ListAccessDevices()
	if err != nil || len(devices) != 1 || devices[0].ID != "device-1" {
		t.Fatalf("ListAccessDevices() = %+v, %v", devices, err)
	}
	if err := client.RevokeAccessDevice("device-1"); err != nil {
		t.Fatalf("RevokeAccessDevice(): %v", err)
	}

	want := []string{
		"GET /api/v1/enrollments?status=pending",
		"POST /api/v1/enrollments/approve",
		"GET /api/v1/devices",
		"DELETE /api/v1/devices/device-1",
	}
	if len(requests) != len(want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
	for i := range want {
		if requests[i] != want[i] {
			t.Fatalf("requests[%d] = %q, want %q", i, requests[i], want[i])
		}
	}
}
