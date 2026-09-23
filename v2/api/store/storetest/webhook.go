package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// RunWebhookStoreConformance is the backend-neutral behavioral contract for
// store.WebhookStore — insert with defaults, merge-on-update of payload/metadata,
// (nil, nil) for a missing get, filtered newest-first listing, and metrics
// grouped by provider/status.
func RunWebhookStoreConformance(t *testing.T, newStore func(t *testing.T) store.WebhookStore) {
	ctx := context.Background()

	newDelivery := func(provider, app, status string) *model.WebhookDelivery {
		return &model.WebhookDelivery{
			ID: uuid.NewString(), Provider: provider, App: app, Status: status,
			Payload: map[string]interface{}{"a": "1"}, Metadata: map[string]interface{}{"m": "1"},
			ReceivedAt: time.Now(),
		}
	}

	t.Run("InsertDefaultsAndGet", func(t *testing.T) {
		s := newStore(t)
		d := &model.WebhookDelivery{ID: uuid.NewString(), Provider: "github"}
		if err := s.InsertWebhookDelivery(ctx, d); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetWebhookDelivery(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatal("get returned nil for an existing delivery")
		}
		if got.Status != "received" {
			t.Fatalf("default status = %q, want received", got.Status)
		}
		if got.Payload == nil || got.Metadata == nil {
			t.Fatal("payload/metadata should be non-nil maps")
		}
		if got.ReceivedAt.IsZero() {
			t.Fatal("receivedAt should default to now")
		}
	})

	t.Run("MissingGetIsNilNil", func(t *testing.T) {
		s := newStore(t)
		got, err := s.GetWebhookDelivery(ctx, "does-not-exist")
		if err != nil {
			t.Fatalf("missing get should not error, got %v", err)
		}
		if got != nil {
			t.Fatal("missing get should return nil delivery")
		}
	})

	t.Run("UpdateMergesPayloadAndMetadata", func(t *testing.T) {
		s := newStore(t)
		d := newDelivery("github", "web", "received")
		if err := s.InsertWebhookDelivery(ctx, d); err != nil {
			t.Fatal(err)
		}
		patch := &model.WebhookDelivery{
			ID: d.ID, Status: "processed", Reason: "ok",
			Payload:  map[string]interface{}{"b": "2"},
			Metadata: map[string]interface{}{"n": "2"},
		}
		if err := s.UpdateWebhookDelivery(ctx, patch); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetWebhookDelivery(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "processed" || got.Reason != "ok" {
			t.Fatalf("update did not apply status/reason: %q %q", got.Status, got.Reason)
		}
		if got.Provider != "github" {
			t.Fatalf("provider not preserved through update: %q", got.Provider)
		}
		if got.Payload["a"] != "1" || got.Payload["b"] != "2" {
			t.Fatalf("payload not merged: %v", got.Payload)
		}
		if got.Metadata["m"] != "1" || got.Metadata["n"] != "2" {
			t.Fatalf("metadata not merged: %v", got.Metadata)
		}
	})

	t.Run("UpdateMissingIsNoop", func(t *testing.T) {
		s := newStore(t)
		err := s.UpdateWebhookDelivery(ctx, &model.WebhookDelivery{ID: "nope", Status: "x", Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}})
		if err != nil {
			t.Fatalf("update of a missing delivery should be a silent no-op, got %v", err)
		}
	})

	t.Run("ListFiltersAndOrders", func(t *testing.T) {
		s := newStore(t)
		older := newDelivery("github", "web", "received")
		older.ReceivedAt = time.Now().Add(-time.Hour)
		newer := newDelivery("github", "api", "processed")
		newer.ReceivedAt = time.Now()
		other := newDelivery("gitlab", "web", "received")
		for _, d := range []*model.WebhookDelivery{older, newer, other} {
			if err := s.InsertWebhookDelivery(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
		github, err := s.ListWebhookDeliveries(ctx, store.WebhookFilter{Provider: "github"})
		if err != nil {
			t.Fatal(err)
		}
		if len(github) != 2 {
			t.Fatalf("provider filter returned %d, want 2", len(github))
		}
		if github[0].ID != newer.ID {
			t.Fatal("list not newest-first")
		}
		web, err := s.ListWebhookDeliveries(ctx, store.WebhookFilter{App: "web"})
		if err != nil {
			t.Fatal(err)
		}
		if len(web) != 2 {
			t.Fatalf("app filter returned %d, want 2", len(web))
		}
	})

	t.Run("MetricsGroupByProviderStatus", func(t *testing.T) {
		s := newStore(t)
		for _, d := range []*model.WebhookDelivery{
			newDelivery("github", "web", "received"),
			newDelivery("github", "web", "received"),
			newDelivery("github", "web", "processed"),
		} {
			if err := s.InsertWebhookDelivery(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
		metrics, err := s.WebhookMetrics(ctx)
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int64{}
		for _, m := range metrics {
			counts[m.Provider+"/"+m.Status] = m.Count
		}
		if counts["github/received"] != 2 || counts["github/processed"] != 1 {
			t.Fatalf("metric counts wrong: %v", counts)
		}
	})
}
