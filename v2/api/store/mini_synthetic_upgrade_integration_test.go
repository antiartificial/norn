package store

import (
	"context"
	"errors"
	"testing"
)

// This deliberately synthetic fixture exercises common Mini control records.
// It contains no copied Mini rows, keys, endpoints, or credentials.
func TestSyntheticMiniControlUpgradeAndReaderBoundary(t *testing.T) {
	pool := schemaMigrationTestPools(t, 1)[0]
	ctx := context.Background()
	migrations := ControlSchemaMigrations()
	if _, err := pool.Exec(ctx, migrations[0].SQL); err != nil {
		t.Fatal(err)
	}
	const fixture = `
		INSERT INTO access_devices(id,name,public_key) VALUES ('device-synthetic','operator','synthetic-public-key');
		INSERT INTO access_tokens(jti,device_id,subject,scopes,issued_at,expires_at)
			VALUES ('token-synthetic','device-synthetic','fixture-operator','["app:read"]',now(),now()+interval '1 day');
		INSERT INTO operations(id,kind,app,status,payload,metadata,lock_generation)
			VALUES ('operation-synthetic','app.deploy','synthetic-app','succeeded','{"image":"sha256:synthetic"}','{"receipt":"synthetic-receipt"}',4);
		INSERT INTO deployments(id,app,commit_sha,image_tag,saga_id,status)
			VALUES ('deployment-synthetic','synthetic-app','synthetic-commit','sha256:synthetic','saga-synthetic','succeeded');
		INSERT INTO deployment_regions(deployment_id,region,nomad_region,status,desired_weight,active_weight)
			VALUES ('deployment-synthetic','synthetic-region','synthetic-nomad-region','succeeded',100,100);
		INSERT INTO deployment_steps(deployment_id,app,saga_id,step,status,metadata)
			VALUES ('deployment-synthetic','synthetic-app','saga-synthetic','promote','succeeded','{"marker":"synthetic-step"}');
		INSERT INTO cron_states(app,process,paused,schedule)
			VALUES ('synthetic-app','hourly',true,'0 * * * *');
		INSERT INTO webhook_deliveries(id,provider,delivery_id,app,status,payload)
			VALUES ('webhook-synthetic','synthetic','delivery-synthetic','synthetic-app','processed','{"marker":"synthetic-webhook"}');
		INSERT INTO control_events(type,app_id,payload)
			VALUES ('synthetic.event','synthetic-app','{"marker":"synthetic-event"}');`
	if _, err := pool.Exec(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	// These are legacy-shape reads: they select only columns present before
	// the migration ledger, including original IDs and receipt bytes.
	const legacyRead = `SELECT jsonb_build_object(
		'token', (SELECT jsonb_build_object('jti',jti,'device',device_id,'scopes',scopes) FROM access_tokens WHERE jti='token-synthetic'),
		'operation', (SELECT jsonb_build_object('id',id,'status',status,'receipt',metadata->>'receipt','generation',lock_generation) FROM operations WHERE id='operation-synthetic'),
		'deployment', (SELECT jsonb_build_object('id',id,'image',image_tag,'status',status) FROM deployments WHERE id='deployment-synthetic'),
		'region', (SELECT jsonb_build_object('region',region,'weight',active_weight) FROM deployment_regions WHERE deployment_id='deployment-synthetic'),
		'step', (SELECT jsonb_build_object('step',step,'marker',metadata->>'marker') FROM deployment_steps WHERE deployment_id='deployment-synthetic'),
		'cron', (SELECT jsonb_build_object('process',process,'paused',paused,'schedule',schedule) FROM cron_states WHERE app='synthetic-app'),
		'webhook', (SELECT jsonb_build_object('id',id,'marker',payload->>'marker') FROM webhook_deliveries WHERE id='webhook-synthetic'),
		'event', (SELECT payload->>'marker' FROM control_events WHERE app_id='synthetic-app')
	)::text`
	var before, after string
	if err := pool.QueryRow(ctx, legacyRead).Scan(&before); err != nil {
		t.Fatal(err)
	}
	migrator, err := NewControlSchemaMigrator(&DB{Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	status, err := migrator.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.CurrentMigrationVersion != 17 || len(status.AppliedVersions) != 17 {
		t.Fatalf("migration status = %+v", status)
	}
	if err := pool.QueryRow(ctx, legacyRead).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("synthetic legacy reads changed across migration: before=%s after=%s", before, after)
	}
	// Migration 17 explicitly retires the preceding reader contract. A
	// rollback to that reader must refuse startup even though its SQL still
	// happens to work against these rows.
	oldReader, err := NewSchemaMigrator(pool, migrations[:16], BinarySchemaCompatibility{ReaderVersion: 2, WriterVersion: 13}, SchemaMigratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = oldReader.Check(ctx, SchemaAccessReadOnly)
	var incompatible *SchemaCompatibilityError
	if !errors.As(err, &incompatible) || incompatible.Contract != "reader" || incompatible.Required != 3 {
		t.Fatalf("old reader check = %T %v, want reader compatibility refusal", err, err)
	}
	if _, err := migrator.Check(ctx, SchemaAccessReadWrite); err != nil {
		t.Fatalf("current reader/writer check: %v", err)
	}
	repeat, err := migrator.Migrate(ctx)
	if err != nil || len(repeat.AppliedVersions) != 0 {
		t.Fatalf("repeat migration status=%+v error=%v", repeat, err)
	}
}
