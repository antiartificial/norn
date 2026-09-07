package handler

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"norn/v2/api/config"
)

func TestFleetAuthorityHumanEnrollmentScopes(t *testing.T) {
	h := &Handler{cfg: &config.Config{FleetAuthorityOnly: true}}
	for _, scopes := range [][]string{nil, {ScopeAPIRead}, {ScopeAPIRead, ScopeAPIWrite}} {
		got, err := h.deviceEnrollmentScopes(scopes)
		if err != nil {
			t.Fatal(err)
		}
		want := scopes
		if len(want) == 0 {
			want = []string{ScopeAPIRead}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	for _, scope := range AccessTokenScopeNames() {
		if scope == ScopeAPIRead || scope == ScopeAPIWrite {
			continue
		}
		if _, err := h.deviceEnrollmentScopes([]string{ScopeAPIRead, scope}); err == nil {
			t.Fatalf("human enrollment accepted %s", scope)
		}
		if _, _, err := h.issueDeviceToken("device", "engineer", []string{scope}, "previous"); err == nil {
			t.Fatalf("token issuance/rotation accepted %s", scope)
		}
	}
}

func TestFleetAuthorityEnrollmentRejectsRunnerScopeBeforeStorage(t *testing.T) {
	h := &Handler{cfg: &config.Config{FleetAuthorityOnly: true}}
	req := httptest.NewRequest(http.MethodPost, "https://authority.example/api/v1/enrollments", strings.NewReader(`{"deviceName":"Engineer Mac","requestedScopes":["fleet:operate"]}`))
	rec := httptest.NewRecorder()
	h.StartDeviceEnrollment(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_scope") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRuntimeEnrollmentDefaultUnchanged(t *testing.T) {
	h := &Handler{cfg: &config.Config{}}
	scopes, err := h.deviceEnrollmentScopes(nil)
	if err != nil || !reflect.DeepEqual(scopes, []string{ScopeAPIRead, ScopeEventsRead}) {
		t.Fatalf("runtime default=%v err=%v", scopes, err)
	}
}
