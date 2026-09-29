package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"

	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/database"
	"norn/v2/api/hub"
	"norn/v2/api/internal/pgtest"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

const deployCanary = "NORN_DEPLOY_CANARY_77b"

// fakeNomad implements the parts of Nomad's HTTP API a deploy uses: job
// registration, job allocations (reported running and healthy) and the
// /v1/var CAS contract. It is not a Nomad agent: it does not render
// templates, enforce workload identity or restart tasks.
type fakeNomad struct {
	mu        sync.Mutex
	variables map[string]*nomadapi.Variable
	index     uint64
	varWrites int
	jobs      map[string]*nomadapi.Job
	bodies    []string
}

func (f *fakeNomad) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/var/"):
		path := strings.TrimPrefix(r.URL.Path, "/v1/var/")
		current := f.variables[path]
		switch r.Method {
		case http.MethodGet:
			if current == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(current)
		case http.MethodPut:
			want, _ := strconv.ParseUint(r.URL.Query().Get("cas"), 10, 64)
			have := uint64(0)
			if current != nil {
				have = current.ModifyIndex
			}
			if want != have {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(current)
				return
			}
			var variable nomadapi.Variable
			_ = json.Unmarshal(body, &variable)
			f.index++
			f.varWrites++
			variable.ModifyIndex = f.index
			f.variables[path] = &variable
			_ = json.NewEncoder(w).Encode(variable)
		}
	case r.URL.Path == "/v1/jobs" && r.Method == http.MethodPut:
		var request struct{ Job *nomadapi.Job }
		if err := json.Unmarshal(body, &request); err != nil || request.Job == nil {
			http.Error(w, "bad job", http.StatusBadRequest)
			return
		}
		f.jobs[*request.Job.ID] = request.Job
		f.bodies = append(f.bodies, string(body))
		_ = json.NewEncoder(w).Encode(map[string]string{"EvalID": uuid.NewString()})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/job/") && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/v1/job/"), "/"):
		// Job info: registered jobs exist (the target guard checks this).
		if job := f.jobs[strings.TrimPrefix(r.URL.Path, "/v1/job/")]; job != nil {
			_ = json.NewEncoder(w).Encode(job)
			return
		}
		http.NotFound(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/job/") && strings.HasSuffix(r.URL.Path, "/allocations"):
		healthy := true
		_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{{ID: uuid.NewString(), TaskGroup: "web", ClientStatus: "running", DesiredStatus: "run", NodeID: "node-1",
			DeploymentStatus: &nomadapi.AllocDeploymentStatus{Healthy: &healthy}}})
	default:
		http.NotFound(w, r)
	}
}

// render executes a submitted task's templates the way Nomad's template
// runner would read them (text/template with nomadVar returning the job's
// variable items, missing keys fatal) and returns the environment the task
// would start with. It is a stand-in for the agent, used only to prove what
// Norn delivered and submitted is sufficient for an ordinary client.
func (f *fakeNomad) render(t *testing.T, task *nomadapi.Task, secretsDir string) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	functions := template.FuncMap{"toJSON": func(value any) (string, error) {
		encoded, err := json.Marshal(value)
		return string(encoded), err
	}, "nomadVar": func(path string) (map[string]struct{ Value string }, error) {
		variable := f.variables[path]
		if variable == nil {
			return nil, fmt.Errorf("variable %s missing", path)
		}
		items := make(map[string]struct{ Value string }, len(variable.Items))
		for key, value := range variable.Items {
			items[key] = struct{ Value string }{Value: value}
		}
		return items, nil
	}}
	environment := []string{}
	for key, value := range task.Env {
		environment = append(environment, key+"="+strings.ReplaceAll(value, "${NOMAD_SECRETS_DIR}", secretsDir))
	}
	for _, tmpl := range task.Templates {
		parsed, err := template.New("t").Funcs(functions).Option("missingkey=error").Parse(*tmpl.EmbeddedTmpl)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := parsed.Execute(&out, nil); err != nil {
			t.Fatalf("render %s: %v", *tmpl.DestPath, err)
		}
		if tmpl.Envvars != nil && *tmpl.Envvars {
			for _, line := range strings.Split(out.String(), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					key, value, ok := strings.Cut(line, "=")
					if !ok {
						t.Fatalf("rendered environment line has no assignment: %q", line)
					}
					if decoded, err := strconv.Unquote(value); err == nil {
						line = key + "=" + decoded
					}
					environment = append(environment, line)
				}
			}
			continue
		}
		destination := filepath.Join(secretsDir, strings.TrimPrefix(*tmpl.DestPath, "secrets/"))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, out.Bytes(), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	return environment
}

type databaseDeployFixture struct {
	t          *testing.T
	db         *store.DB
	pipeline   *pipeline.Pipeline
	worker     *OperationWorker
	nomad      *fakeNomad
	request    pipeline.EnqueueRequest
	spec       *model.InfraSpec
	serverA    *pgtest.Server
	serverB    *pgtest.Server
	snapshots  string
	catalog    database.Catalog
	node       string
	nodeClient string
	secretDir  string
}

func newDatabaseDeployFixture(t *testing.T) *databaseDeployFixture {
	t.Helper()
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	for _, tool := range []string{"git", "pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required", tool)
		}
	}
	node, nodeClient := pgtest.NodeClientScript(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "norn_db_deploy_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA `+identifier+` CASCADE`); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		admin.Close()
	})
	db := &store.DB{Pool: pool}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}

	// Two servers with identical database, role and password. Only the
	// marker distinguishes them; ambient settings will point at A.
	servers := map[string]*pgtest.Server{}
	for _, name := range []string{"server-a", "server-b"} {
		server := pgtest.Start(t)
		server.CreateDatabase(t, "shop")
		server.CreatePasswordRole(t, "shop_app", deployCanary, "shop")
		server.Exec(t, "shop", fmt.Sprintf(`CREATE TABLE marker (value text); INSERT INTO marker VALUES ('%s');
			CREATE TABLE migration_log (db text, role text, via text); ALTER TABLE marker OWNER TO shop_app; ALTER TABLE migration_log OWNER TO shop_app`, name))
		servers[name] = server
	}
	secretDir := t.TempDir()
	if err := os.Chmod(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "shop"), []byte(fmt.Sprintf(`{"password":%q}`, deployCanary)), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets, err := database.NewDirectorySecretSource(secretDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secrets.Close() })

	app := "shop-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, app)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The migration is an ordinary Node process reading DATABASE_URL (the
	// connection value) and DATABASE_URL_FILE (a path), recording where it
	// actually connected.
	migration := fmt.Sprintf(`%q %q value "INSERT INTO migration_log SELECT current_database(), current_user, 'value'" && %q %q file "INSERT INTO migration_log SELECT current_database(), current_user, 'file'"`, node, nodeClient, node, nodeClient)
	specText := fmt.Sprintf(`schemaVersion: norn.app/v2
name: %s
deploy: true
build:
  image: registry.example.test/%s:fixture
processes:
  web:
    command: node server.js
  nightly:
    command: node report.js
    schedule: "0 3 * * *"
databases:
  - name: primary
    purpose: application
    capabilities: [runtime, migration, snapshot, restore, health]
    runtime:
      env: DATABASE_URL
      fileEnv: DATABASE_URL_FILE
migrations: %q
`, app, app, migration)
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte(specText), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=norn-test", "-c", "user.email=norn-test@example.invalid", "commit", "-q", "-m", "fixture"}} {
		command := exec.Command("git", append([]string{"-C", appDir}, args...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	spec, err := model.LoadInfraSpec(filepath.Join(appDir, "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeNomad{variables: map[string]*nomadapi.Variable{}, jobs: map[string]*nomadapi.Job{}}
	nomadServer := httptest.NewServer(fake)
	t.Cleanup(nomadServer.Close)
	nomadClient, err := nomad.NewClient(nomadServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := store.NewHMACAcceptanceSigner("database-deploy-test-signing-key-0000000000")
	if err != nil {
		t.Fatal(err)
	}
	operationStore, err := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := operationStore.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ws := hub.New(nil)
	go ws.Run()
	snapshots := t.TempDir()
	p := &pipeline.Pipeline{
		DB: db, OperationStore: operationStore, WS: ws, SagaStore: saga.NewPostgresStore(pool), AppsDir: appsDir, NetworkMode: "local", Nomad: nomadClient,
		DatabaseTargets: &pipeline.DatabaseTargets{ProfileID: "mini", Catalog: db.ActiveDatabaseCatalog, Secrets: secrets, SnapshotRoot: snapshots},
		RunBuildCommand: func(context.Context, string, ...string) ([]byte, error) {
			return nil, fmt.Errorf("the fixture uses a prebuilt image")
		},
	}
	service := func(id string, server *pgtest.Server) database.DatabaseService {
		return database.DatabaseService{APIVersion: database.APIVersion, ID: id, Generation: 1, Purpose: database.PurposeApplication, Engine: database.EnginePostgreSQL,
			EngineVersion: "16", ProviderRef: "local:" + id, Endpoint: database.DatabaseEndpoint{Host: server.SocketDir, Port: server.Port},
			Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled},
			Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilityRuntime, database.CapabilityMigration, database.CapabilitySnapshot, database.CapabilityRestore, database.CapabilityHealth}}}
	}
	binding := func(id, serviceID string) database.DatabaseBinding {
		return database.DatabaseBinding{APIVersion: database.APIVersion, ID: id, ServiceID: serviceID, Database: "shop", Role: "shop_app", Generation: 1, CredentialRef: "secret:shop", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}}
	}
	catalog := database.Catalog{APIVersion: database.APIVersion,
		Services: []database.DatabaseService{service("pg-a", servers["server-a"]), service("pg-b", servers["server-b"])},
		Bindings: []database.DatabaseBinding{binding("shop-on-a", "pg-a"), binding("shop-primary", "pg-b")},
		Profiles: []database.DeploymentProfile{{APIVersion: database.APIVersion, ID: "mini", Topology: database.DeploymentTopologyLocal, AvailabilityClass: database.AvailabilitySingleHost,
			DatabaseBindings: map[string]string{"primary": "shop-primary"}}},
	}
	worker := &OperationWorker{db: db, pipeline: p, id: "database-deploy-worker", kinds: []string{"app.deploy", "app.snapshot", "app.snapshot-restore", "app.migrate"}, lease: time.Minute, poll: time.Second}
	request := pipeline.EnqueueRequest{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/test", Subject: "operator"}, Key: "deploy-1", Audit: store.AcceptanceAuditContext{Source: "database-deploy-test"}}
	return &databaseDeployFixture{t: t, db: db, pipeline: p, worker: worker, nomad: fake, request: request, spec: spec,
		serverA: servers["server-a"], serverB: servers["server-b"], snapshots: snapshots, catalog: catalog, node: node, nodeClient: nodeClient, secretDir: secretDir}
}

func (f *databaseDeployFixture) runDue(op string) *model.Operation {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET next_attempt_at = CASE WHEN id=$1 THEN now() - interval '1 second' ELSE now() + interval '1 hour' END WHERE status='queued'`, op); err != nil {
		f.t.Fatal(err)
	}
	if err := f.worker.runOnce(ctx); err != nil {
		f.t.Fatal(err)
	}
	current, err := f.db.GetOperation(ctx, op)
	if err != nil {
		f.t.Fatal(err)
	}
	return current
}

func (f *databaseDeployFixture) migrationLog(server *pgtest.Server) []string {
	f.t.Helper()
	connection, err := pgx.Connect(context.Background(), server.URL("shop"))
	if err != nil {
		f.t.Fatal(err)
	}
	defer connection.Close(context.Background())
	rows, err := connection.Query(context.Background(), `SELECT db || '/' || role || '/' || via FROM migration_log ORDER BY via`)
	if err != nil {
		f.t.Fatal(err)
	}
	entries, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		f.t.Fatal(err)
	}
	return entries
}

// runTask renders the submitted task as an allocation would see it and runs
// an ordinary Node client in that environment only.
func (f *databaseDeployFixture) runTask(jobID, mode, query string) (string, error) {
	f.t.Helper()
	f.nomad.mu.Lock()
	job := f.nomad.jobs[jobID]
	f.nomad.mu.Unlock()
	if job == nil {
		f.t.Fatalf("job %s was not submitted", jobID)
	}
	environment := f.nomad.render(f.t, job.TaskGroups[0].Tasks[0], f.t.TempDir())
	command := exec.Command(f.node, f.nodeClient, mode, query)
	command.Env = append([]string{"PATH=" + os.Getenv("PATH")}, environment...)
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func TestNamedDatabaseDeployThroughAcceptanceAndWorkerUsesDeclaredServer(t *testing.T) {
	f := newDatabaseDeployFixture(t)
	ctx := context.Background()
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 0, f.catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	accepted, err := f.pipeline.Run(ctx, f.spec, "HEAD", f.request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(accepted.Operation.Payload["databaseTargets"]), `"bindingId":"shop-primary"`) {
		t.Fatalf("accepted payload = %v", accepted.Operation.Payload)
	}
	// Every ambient default now names server A with a valid credential, so
	// any fallback to inherited routing would succeed against the wrong server.
	t.Setenv("PGHOST", f.serverA.SocketDir)
	t.Setenv("PGPORT", strconv.Itoa(f.serverA.Port))
	t.Setenv("PGUSER", "shop_app")
	t.Setenv("PGDATABASE", "shop")
	t.Setenv("PGPASSWORD", deployCanary)
	t.Setenv("DATABASE_URL", "postgresql://shop_app:"+deployCanary+"@localhost/shop?host="+f.serverA.SocketDir+"&port="+strconv.Itoa(f.serverA.Port)+"&sslmode=disable")

	done := f.runDue(accepted.Operation.ID)
	if done.Status != model.OperationSucceeded {
		t.Fatalf("deploy = %s: %s", done.Status, done.Message)
	}
	// The Node migration ran on B through both forms, never on A.
	if log := f.migrationLog(f.serverB); strings.Join(log, ",") != "shop/shop_app/file,shop/shop_app/value" {
		t.Fatalf("server B migration log = %v", log)
	}
	if log := f.migrationLog(f.serverA); len(log) != 0 {
		t.Fatalf("migration reached server A: %v", log)
	}
	// The safety snapshot is in B's target namespace with B's identity.
	var sidecars []string
	_ = filepath.WalkDir(f.snapshots, func(path string, entry os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".target.json") {
			sidecars = append(sidecars, path)
		}
		return nil
	})
	if len(sidecars) != 1 || !strings.Contains(sidecars[0], filepath.Join("targets", "")) {
		t.Fatalf("snapshot sidecars = %v", sidecars)
	}
	if data, _ := os.ReadFile(sidecars[0]); !strings.Contains(string(data), `"serviceId":"pg-b"`) {
		t.Fatalf("sidecar = %s", data)
	}

	// Delivery: jobs carry no credential; templates read the revision staged
	// for this deploy, promoted after readiness.
	f.nomad.mu.Lock()
	for _, body := range f.nomad.bodies {
		if strings.Contains(body, deployCanary) || strings.Contains(body, "postgresql://") {
			f.nomad.mu.Unlock()
			t.Fatal("a submitted job embeds the connection")
		}
	}
	service := f.nomad.variables[nomad.DatabaseVariablePath(f.spec.App)]
	periodic := f.nomad.variables[nomad.DatabaseVariablePath(f.spec.App+"-nightly")]
	f.nomad.mu.Unlock()
	if service == nil || periodic == nil || service.Items["norn_delivery_catalog_revision"] != "1" || service.Items[nomad.DatabaseItemKey("primary")] == "" {
		t.Fatalf("delivery variables = %+v / %+v", service, periodic)
	}
	// Web and cron tasks, rendered as their allocations would be, reach B
	// with an ordinary client through the value and the file.
	for _, job := range []string{f.spec.App, f.spec.App + "-nightly"} {
		for _, mode := range []string{"value", "file"} {
			if output, err := f.runTask(job, mode, "SELECT value, current_user FROM marker"); err != nil || output != "server-b\tshop_app" {
				t.Fatalf("%s %s client = %q, %v", job, mode, output, err)
			}
		}
	}

	// An identical retry returns the original receipt without new work.
	retry, err := f.pipeline.Run(ctx, f.spec, "HEAD", f.request)
	if err != nil || !retry.Replayed || retry.Operation.ID != accepted.Operation.ID {
		t.Fatalf("identical retry = %+v, %v", retry, err)
	}

	sideEffects := func() (int, int, int, int) {
		f.nomad.mu.Lock()
		defer f.nomad.mu.Unlock()
		return f.nomad.varWrites, len(f.nomad.bodies), len(f.migrationLog(f.serverA)), len(f.migrationLog(f.serverB))
	}
	deploy := func(key string) (*model.Operation, error) {
		t.Helper()
		request := f.request
		request.Key = key
		accepted, err := f.pipeline.Run(ctx, f.spec, "HEAD", request)
		if err != nil {
			return nil, err
		}
		return &accepted.Operation, nil
	}

	// Credential-only rotation (same target identity) is an ordinary
	// deploy: new material is staged and promoted for the same target.
	if err := os.WriteFile(filepath.Join(f.secretDir, "shop-rotated"), []byte(fmt.Sprintf(`{"password":%q}`, deployCanary)), 0o600); err != nil {
		t.Fatal(err)
	}
	rotated := f.catalog
	rotated.Bindings = append([]database.DatabaseBinding(nil), f.catalog.Bindings...)
	rotated.Bindings[1].CredentialRef = "secret:shop-rotated"
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 1, rotated, "operator"); err != nil {
		t.Fatal(err)
	}
	rotation, err := deploy("deploy-2")
	if err != nil {
		t.Fatal(err)
	}
	if done := f.runDue(rotation.ID); done.Status != model.OperationSucceeded {
		t.Fatalf("credential-rotation deploy = %s: %s", done.Status, done.Message)
	}
	f.nomad.mu.Lock()
	promoted := f.nomad.variables[nomad.DatabaseVariablePath(f.spec.App)].Items["norn_delivery_catalog_revision"]
	f.nomad.mu.Unlock()
	if promoted != "2" {
		t.Fatalf("promoted revision after rotation = %s", promoted)
	}
	if output, err := f.runTask(f.spec.App, "value", "SELECT value FROM marker"); err != nil || output != "server-b" {
		t.Fatalf("rotated deploy client = %q, %v", output, err)
	}
	// Rollback, cron resubmission and functions reference the promoted
	// revision only after revalidating it against the running targets.
	for _, job := range []string{f.spec.App, f.spec.App + "-nightly"} {
		material, err := f.pipeline.RunningDeliveryRevision(ctx, f.spec, "global", job)
		if err != nil || material.Revision != 2 || !strings.Contains(material.Targets["primary"], `"serviceId":"pg-b"`) {
			t.Fatalf("running delivery revision of %s = %+v, %v", job, material, err)
		}
	}

	// Execution-time guard: a same-target deploy is accepted, then history
	// gains a possible writer on another target (a partial rollout to pg-a
	// that failed after job submission) before it runs. Execution refuses
	// before snapshot, migration, delivery or registration.
	raced, err := deploy("deploy-5")
	if err != nil {
		t.Fatal(err)
	}
	candidate := &model.Operation{ID: uuid.NewString(), App: f.spec.App, Kind: "app.deploy", Status: model.OperationFailed, SagaID: uuid.NewString(), MaxAttempts: 1, Attempts: 1,
		Metadata: map[string]interface{}{"step": "healthy"},
		Payload:  map[string]interface{}{"databaseTargets": `{"schema":"norn.database-targets/v1","profileId":"mini","catalogRevision":3,"targets":[{"name":"primary","target":{"serviceId":"pg-a","serviceGeneration":1,"bindingId":"shop-primary","bindingGeneration":2,"engine":"postgresql","database":"shop","role":"shop_app"}}]}`}}
	if err := f.db.InsertCompletedOperation(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	racedWrites, racedJobs, racedLogA, racedLogB := sideEffects()
	if done := f.runDue(raced.ID); done.Status == model.OperationSucceeded || !strings.Contains(done.Message, "cutover lane") {
		t.Fatalf("deploy beside an in-flight candidate on another target = %s: %s", done.Status, done.Message)
	}
	if writes, jobs, logA, logB := sideEffects(); writes != racedWrites || jobs != racedJobs || logA != racedLogA || logB != racedLogB {
		t.Fatal("the execution-time guard allowed side effects")
	}
	// Resolve the simulated candidate as writer-free for the rest of the test.
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET metadata = jsonb_set(metadata, '{step}', '"build"') WHERE id=$1`, candidate.ID); err != nil {
		t.Fatal(err)
	}

	// A deploy accepted before the target moves is fenced as stale.
	beforeMove, err := deploy("deploy-3")
	if err != nil {
		t.Fatal(err)
	}
	// The catalog moves primary to the other server (a new target).
	moved := rotated
	primary := rotated.Bindings[1]
	primary.ServiceID, primary.Generation = "pg-a", 2
	moved.Bindings = []database.DatabaseBinding{primary}
	moved.Retired = []database.RetiredResource{{Kind: database.RetiredBinding, ID: "shop-on-a"}}
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 2, moved, "operator"); err != nil {
		t.Fatal(err)
	}
	writes0, jobs0, logA0, logB0 := sideEffects()
	if done := f.runDue(beforeMove.ID); done.Status == model.OperationSucceeded || !strings.Contains(done.Message, "differs from the expected target") {
		t.Fatalf("stale deploy = %s: %s", done.Status, done.Message)
	}
	// A deploy accepted after the move is refused at acceptance: the running
	// app's writers are on B; an ordinary deploy may not start writers on A.
	var targetErr *pipeline.DatabaseTargetError
	if _, err := deploy("deploy-4"); !errors.As(err, &targetErr) || !strings.Contains(err.Error(), "cutover lane") {
		t.Fatalf("target-moving deploy acceptance = %v", err)
	}
	// Zero new-target registrations, variable writes or migrations anywhere,
	// while the old-target (B) material stays promoted.
	if writes, jobs, logA, logB := sideEffects(); writes != writes0 || jobs != jobs0 || logA != logA0 || logB != logB0 || logA != 0 {
		t.Fatalf("refused target moves produced side effects: writes %d→%d jobs %d→%d migrations A %d→%d B %d→%d", writes0, writes, jobs0, jobs, logA0, logA, logB0, logB)
	}
	f.nomad.mu.Lock()
	for _, job := range f.nomad.jobs {
		encoded, _ := json.Marshal(job)
		if strings.Contains(string(encoded), "norn_rev3_") {
			f.nomad.mu.Unlock()
			t.Fatalf("a job referencing the new-target revision was registered: %s", *job.ID)
		}
	}
	f.nomad.mu.Unlock()
	if output, err := f.runTask(f.spec.App, "value", "SELECT value FROM marker"); err != nil || output != "server-b" {
		t.Fatalf("running writers after refused move = %q, %v", output, err)
	}

	// No persisted operation, deployment or saga record carries the credential.
	var leaked int
	if err := f.db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operations WHERE payload::text LIKE $1 OR metadata::text LIKE $1 OR message LIKE $1 OR last_error LIKE $1)
		+ (SELECT count(*) FROM saga_events WHERE message LIKE $1 OR metadata::text LIKE $1)
		+ (SELECT count(*) FROM deployments WHERE row_to_json(deployments)::text LIKE $1)`, "%"+deployCanary+"%").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("credential persisted %d times (%v)", leaked, err)
	}
}
