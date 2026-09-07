package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/store"
)

func TestBrowserEnrollmentTokenLifetimeIsShort(t *testing.T) {
	h := &Handler{cfg: &config.Config{FleetAuthorityOnly: true, APIToken: strings.Repeat("x", 32)}}
	_, record, err := h.issueDeviceTokenWithTTL("browser-device", "NornUI browser", []string{ScopeAPIRead}, "", deviceTokenLifetimeForPlatform("web"))
	if err != nil {
		t.Fatal(err)
	}
	if got := record.ExpiresAt.Sub(record.IssuedAt); got != browserDeviceTokenTTL {
		t.Fatalf("browser token lifetime=%s want=%s", got, browserDeviceTokenTTL)
	}
	_, native, err := h.issueDeviceToken("native-device", "Norn", []string{ScopeAPIRead}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := native.ExpiresAt.Sub(native.IssuedAt); got != deviceTokenTTL {
		t.Fatalf("native token lifetime=%s want=%s", got, deviceTokenTTL)
	}
}

// Uses the explicit disposable integration database and proves a browser token
// cannot become a 30-day token through rotation. The platform comes from the
// persisted device, not a frontend-controlled request field.
func TestBrowserTokenRotationKeepsStoredShortLifetime(t *testing.T) {
	dsn := os.Getenv("NORN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := store.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	deviceID, tokenID := uuid.NewString(), "browser-"+uuid.NewString()
	if err := db.CreateAccessDevice(t.Context(), &store.AccessDevice{ID: deviceID, Name: "NornUI browser", Platform: "web", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordAccessToken(t.Context(), &store.AccessToken{JTI: tokenID, DeviceID: deviceID, Subject: "NornUI browser", Scopes: []string{ScopeAPIRead}, IssuedAt: now, ExpiresAt: now.Add(browserDeviceTokenTTL)}); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: &config.Config{FleetAuthorityOnly: true, APIToken: strings.Repeat("x", 32)}, db: db}
	req := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/auth/rotate", nil), &AccessPrincipal{Subject: "NornUI browser", DeviceID: deviceID, TokenID: tokenID, Scopes: []string{ScopeAPIRead}})
	rec := httptest.NewRecorder()
	h.RotateCurrentToken(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate status=%d", rec.Code)
	}
	var issued struct {
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&issued); err != nil {
		t.Fatal(err)
	}
	if got := issued.ExpiresAt.Sub(time.Now().UTC()); got > browserDeviceTokenTTL || got < browserDeviceTokenTTL-time.Minute {
		t.Fatalf("rotated browser token remaining lifetime=%s, want approximately %s", got, browserDeviceTokenTTL)
	}
}
