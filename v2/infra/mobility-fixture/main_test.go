package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMobilityFixtureReconcilesRowsFilesWorkAndSchedule(t *testing.T) {
	dsn := os.Getenv("MOBILITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("MOBILITY_TEST_DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// One connection keeps the disposable schema selected for every statement.
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id, err := randomID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "mobility_test_" + id
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
	if _, err := db.ExecContext(ctx, `SET search_path TO `+schema); err != nil {
		t.Fatal(err)
	}
	f := fixture{db: db, files: t.TempDir(), writable: true}
	if err := f.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader("known-body"))
	w := httptest.NewRecorder()
	f.routes().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created struct{ ID, SHA256 string }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if err := f.ackOne(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.tick(ctx, time.Date(2026, 9, 27, 8, 7, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	assertState := func(mismatch, orphan int) {
		t.Helper()
		w := httptest.NewRecorder()
		f.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/state", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("state status=%d body=%s", w.Code, w.Body.String())
		}
		var state struct {
			Items                                          []struct{ ID, SHA256 string }
			JobsPending, JobsAcknowledged, ScheduleTicks   int
			FilesMissing, FilesMismatched, FilesOrphaned   int
			PendingJobIds, AcknowledgedJobIds, TickMinutes []string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if len(state.Items) != 1 || state.Items[0].ID != created.ID || state.Items[0].SHA256 != created.SHA256 ||
			state.JobsPending != 0 || state.JobsAcknowledged != 1 || state.ScheduleTicks != 1 ||
			len(state.PendingJobIds) != 0 || len(state.AcknowledgedJobIds) != 1 || state.AcknowledgedJobIds[0] != created.ID ||
			len(state.TickMinutes) != 1 || state.TickMinutes[0] != "2026-09-27T08:07:00Z" ||
			state.FilesMissing != 0 || state.FilesMismatched != mismatch || state.FilesOrphaned != orphan {
			t.Fatalf("unexpected state: %+v", state)
		}
	}
	assertState(0, 0)
	if err := os.WriteFile(filepath.Join(f.files, created.ID), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.files, "orphan"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertState(1, 1)
	f.writable = false
	w = httptest.NewRecorder()
	f.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/items", strings.NewReader("blocked")))
	if w.Code != http.StatusLocked || f.ackOne(ctx) == nil || f.tick(ctx, time.Now()) == nil {
		t.Fatal("source writer fence did not cover web, worker and schedule")
	}
}
