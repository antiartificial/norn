package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// RunNotificationStoreConformance is the backend-neutral behavioral contract for
// store.NotificationStore — channel insert/get/list/delete with credentials and
// severities preserved intact, and newest-first ordering.
func RunNotificationStoreConformance(t *testing.T, newStore func(t *testing.T) store.NotificationStore) {
	ctx := context.Background()

	t.Run("InsertGetDelete", func(t *testing.T) {
		s := newStore(t)
		ch := &model.NotificationChannel{
			ID: uuid.NewString(), Provider: "pushover", Name: "ops",
			URL: "https://api.pushover.net", Token: "secret-token", UserKey: "user-key",
			Severities: []string{"critical", "warning"}, CreatedAt: time.Now(),
		}
		if err := s.InsertNotificationChannel(ctx, ch); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetNotificationChannel(ctx, ch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Token != "secret-token" || got.UserKey != "user-key" {
			t.Fatalf("credentials not preserved: token=%q userKey=%q", got.Token, got.UserKey)
		}
		if len(got.Severities) != 2 || got.Severities[0] != "critical" {
			t.Fatalf("severities not preserved: %v", got.Severities)
		}
		if err := s.DeleteNotificationChannel(ctx, ch.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetNotificationChannel(ctx, ch.ID); err == nil {
			t.Fatal("get after delete should error")
		}
	})

	t.Run("ListNewestFirst", func(t *testing.T) {
		s := newStore(t)
		older := &model.NotificationChannel{ID: uuid.NewString(), Provider: "discord", Name: "a", CreatedAt: time.Now().Add(-time.Hour)}
		newer := &model.NotificationChannel{ID: uuid.NewString(), Provider: "ntfy", Name: "b", CreatedAt: time.Now()}
		if err := s.InsertNotificationChannel(ctx, older); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertNotificationChannel(ctx, newer); err != nil {
			t.Fatal(err)
		}
		list, err := s.ListNotificationChannels(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 2 {
			t.Fatalf("list returned %d channels, want 2", len(list))
		}
		if list[0].ID != newer.ID {
			t.Fatalf("list not newest-first: got %s first", list[0].ID)
		}
	})
}
