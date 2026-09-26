package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRootAccessTokenPostgres(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := "lineage-root-" + uuid.NewString()
	second := "lineage-second-" + uuid.NewString()
	third := "lineage-third-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM access_tokens WHERE jti = ANY($1)`, []string{root, second, third})
	})
	now := time.Now().UTC()
	for _, token := range []AccessToken{
		{JTI: root, IssuedAt: now, ExpiresAt: now.Add(time.Hour)},
		{JTI: second, RotatedFrom: root, IssuedAt: now, ExpiresAt: now.Add(time.Hour)},
		{JTI: third, RotatedFrom: second, IssuedAt: now, ExpiresAt: now.Add(time.Hour)},
	} {
		if err := db.RecordAccessToken(ctx, &token); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{root, second, third} {
		got, err := db.RootAccessToken(ctx, id)
		if err != nil || got != root {
			t.Fatalf("RootAccessToken(%q) = %q, %v; want %q", id, got, err, root)
		}
	}
	if _, err := db.RootAccessToken(ctx, "missing-"+uuid.NewString()); !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("missing token error = %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE access_tokens SET rotated_from=$1 WHERE jti=$2`, "missing-"+uuid.NewString(), second); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RootAccessToken(ctx, third); !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("missing parent error = %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE access_tokens SET rotated_from=$1 WHERE jti=$2`, third, second); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RootAccessToken(ctx, third); err == nil {
		t.Fatal("cyclic lineage was accepted")
	}
}
