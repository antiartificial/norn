package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/hub"
)

func TestHubEventAppendSerializesResolvedTableCommitOrderAcrossSearchPaths(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	basePool := schemaMigrationTestPools(t, 1)[0]
	base := &DB{Pool: basePool}
	if err := Migrate(base); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var targetSchema string
	if err := basePool.QueryRow(ctx, `SELECT current_schema()`).Scan(&targetSchema); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	emptySchemas := []string{"hub_empty_a_" + suffix, "hub_empty_b_" + suffix}
	for _, schema := range emptySchemas {
		if _, err := basePool.Exec(ctx, `CREATE SCHEMA `+quoteTestIdentifier(schema)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, schema := range emptySchemas {
			_, _ = basePool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+quoteTestIdentifier(schema)+` CASCADE`)
		}
	})

	writers := make([]*DB, 0, 2)
	applicationNames := []string{"hub-append-first-" + suffix, "hub-append-second-" + suffix}
	for index, leadingSchema := range emptySchemas {
		config, err := pgxpool.ParseConfig(databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		config.ConnConfig.RuntimeParams["search_path"] = leadingSchema + "," + targetSchema
		config.ConnConfig.RuntimeParams["application_name"] = applicationNames[index]
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		writers = append(writers, &DB{Pool: pool})
	}
	var schemaA, schemaB string
	var relationA, relationB uint32
	if err := writers[0].Pool.QueryRow(ctx, `SELECT current_schema(), 'control_events'::regclass::oid`).Scan(&schemaA, &relationA); err != nil {
		t.Fatal(err)
	}
	if err := writers[1].Pool.QueryRow(ctx, `SELECT current_schema(), 'control_events'::regclass::oid`).Scan(&schemaB, &relationB); err != nil {
		t.Fatal(err)
	}
	if schemaA == schemaB || relationA != relationB {
		t.Fatalf("search-path fixture schemas=%q/%q relations=%d/%d", schemaA, schemaB, relationA, relationB)
	}

	var before int64
	if err := basePool.QueryRow(ctx, `SELECT COALESCE(MAX(id),0) FROM control_events`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	gapTx, err := writers[0].Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gapTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('norn.control-events.append'), 'control_events'::regclass::oid::integer)`); err != nil {
		_ = gapTx.Rollback(ctx)
		t.Fatal(err)
	}
	var rolledBackID int64
	if err := gapTx.QueryRow(ctx, `INSERT INTO control_events(timestamp,type,payload) VALUES(clock_timestamp(),$1,'{}') RETURNING id`, "hub-gap-"+suffix).Scan(&rolledBackID); err != nil {
		_ = gapTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := gapTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if rolledBackID <= before {
		t.Fatalf("rollback gap id=%d before=%d", rolledBackID, before)
	}

	first, err := writers[0].Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(context.Background())
	if _, err := first.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('norn.control-events.append'), 'control_events'::regclass::oid::integer)`); err != nil {
		t.Fatal(err)
	}
	firstType := "hub-serialized-" + suffix + "-first"
	var firstID int64
	if err := first.QueryRow(ctx, `INSERT INTO control_events(timestamp,type,payload) VALUES(clock_timestamp(),$1,'{}') RETURNING id`, firstType).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		event hub.Event
		err   error
	}
	results := make(chan result, 1)
	secondEvent := hub.Event{Timestamp: time.Now().UTC(), Type: fmt.Sprintf("hub-serialized-%s-second", suffix), Payload: map[string]int{"writer": 2}}
	go func() {
		err := writers[1].AppendHubEvent(context.Background(), &secondEvent)
		results <- result{event: secondEvent, err: err}
	}()
	waitForHubEventAppendWaiter(t, basePool, applicationNames[1])
	var visible int
	if err := basePool.QueryRow(ctx, `SELECT count(*) FROM control_events WHERE type LIKE $1`, "hub-serialized-"+suffix+"-%").Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if visible != 0 {
		t.Fatalf("uncommitted prefix became visible while the next writer waited: %d rows", visible)
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	completed := <-results
	if completed.err != nil {
		t.Fatal(completed.err)
	}
	if firstID <= rolledBackID || completed.event.ID != firstID+1 {
		t.Fatalf("serialized IDs=%d,%d rollbackGap=%d", firstID, completed.event.ID, rolledBackID)
	}
	events, err := base.ListHubEventsAfter(ctx, before, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ID != firstID || events[0].Type != firstType || events[1].ID != completed.event.ID || events[1].Type != completed.event.Type {
		t.Fatalf("visible ordered events=%+v firstID=%d completed=%+v", events, firstID, completed)
	}
}

func waitForHubEventAppendWaiter(t *testing.T, pool *pgxpool.Pool, applicationName string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		err := pool.QueryRow(context.Background(), `
			SELECT EXISTS (
				SELECT 1
			FROM pg_stat_activity
			WHERE datname=current_database()
			  AND application_name=$1
			  AND query LIKE '%/* hub-event-append-lock */%'
			  AND wait_event_type='Lock'
			)
		`, applicationName).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not observe blocked hub-event appender %q", applicationName)
}

func quoteTestIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
