package nomad

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

const deliveryCanary = "NORN_DELIVERY_CANARY_5e1"

func deliverySpec() *model.InfraSpec {
	return &model.InfraSpec{SchemaVersion: model.AppSchemaV2, App: "shop",
		Env: map[string]string{"LOG_LEVEL": "info"},
		Processes: map[string]model.Process{
			"web":     {Port: 3000, Command: "node server.js"},
			"worker":  {Command: "node worker.js", Env: map[string]string{"QUEUE": "jobs"}},
			"nightly": {Command: "node report.js", Schedule: "0 3 * * *"},
			"resize":  {Command: "node resize.js", Function: &model.FunctionSpec{Timeout: "30s"}},
		},
		Databases: []model.DatabaseRequirement{
			{Name: "primary", Purpose: "application", Capabilities: []string{"runtime"}, Runtime: &model.DatabaseRuntime{Env: "DATABASE_URL", FileEnv: "DATABASE_URL_FILE"}},
			{Name: "analytics-db", Purpose: "application", Capabilities: []string{"runtime"}, Runtime: &model.DatabaseRuntime{FileEnv: "ANALYTICS_URL_FILE"}},
			{Name: "archive", Purpose: "application", Capabilities: []string{"snapshot"}},
		},
	}
}

func TestWordPressDatabaseDeliveryIsPrivateAndRevisionBound(t *testing.T) {
	spec := &model.InfraSpec{SchemaVersion: model.AppSchemaV2, App: "wordpress", Processes: map[string]model.Process{
		"web": {Command: "php-fpm"}, "cron": {Command: "wp cron event run", Schedule: "@hourly"},
	}, Databases: []model.DatabaseRequirement{{Name: "primary", Purpose: "application", Capabilities: []string{"runtime"}, Runtime: &model.DatabaseRuntime{
		Components: &model.DatabaseRuntimeComponents{Host: "WORDPRESS_DB_HOST", User: "WORDPRESS_DB_USER", Password: "WORDPRESS_DB_PASSWORD", Name: "WORDPRESS_DB_NAME"},
	}}}}
	for _, job := range []*nomadapi.Job{TranslateForRegionAt(spec, "wordpress:test", nil, spec.ResolvedRegions()[0], 7),
		TranslatePeriodicForRegionAt(spec, "cron", spec.Processes["cron"], "wordpress:test", nil, spec.ResolvedRegions()[0], 7)} {
		for _, group := range job.TaskGroups {
			for _, task := range group.Tasks {
				if len(task.Templates) != 1 || !*task.Templates[0].Envvars || !*task.Templates[0].ErrMissingKey || *task.Templates[0].Perms != "0400" {
					t.Fatalf("%s private template = %+v", *job.ID, task.Templates)
				}
				data := *task.Templates[0].EmbeddedTmpl
				for field, env := range map[string]string{"host": "WORDPRESS_DB_HOST", "user": "WORDPRESS_DB_USER", "password": "WORDPRESS_DB_PASSWORD", "name": "WORDPRESS_DB_NAME"} {
					want := env + "={{ ." + stagedKey(DatabaseComponentItemKey("primary", field), 7) + ".Value | toJSON }}"
					if !strings.Contains(data, want) {
						t.Fatalf("%s lacks staged %s field: %q", *job.ID, field, data)
					}
				}
				if strings.Contains(data, deliveryCanary) || strings.Contains(data, "{{ .norn_db_component_") {
					t.Fatalf("%s leaks value or reads mutable database items", *job.ID)
				}
			}
		}
	}
	fake := &fakeVariables{variables: map[string]*nomadapi.Variable{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]string{DatabaseTargetItemKey("primary"): `{"bindingId":"wordpress","bindingGeneration":1}`}
	for field, value := range map[string]string{"host": "mysql.internal:3306", "user": "wp", "password": deliveryCanary, "name": "wordpress"} {
		items[DatabaseComponentItemKey("primary", field)] = value
	}
	if err := client.DeliverDatabaseVariable("west", "wordpress", items, 7); err != nil {
		t.Fatal(err)
	}
	revision, err := client.ReadDatabaseRevision("west", "wordpress", 7)
	if err != nil || len(revision.URLs) != 0 || len(revision.Components) != 4 || revision.Components[DatabaseComponentItemKey("primary", "password")] != deliveryCanary {
		t.Fatalf("component revision = %+v, %v", revision, err)
	}
	owned, err := client.CopyDatabaseVariable("west", "wordpress-cron-1", revision)
	if err != nil || owned[stagedKey(DatabaseComponentItemKey("primary", "password"), 7)] != deliveryCanary {
		t.Fatal("component delivery was not copied to a one-shot job")
	}
	if err := client.DeleteDatabaseVariable("west", "wordpress-cron-1", owned); err != nil {
		t.Fatal(err)
	}
}

// Every translation path (web and worker services, cron, function) carries
// the same private delivery: templates reading only the job's own variable,
// the value-variable rendered as env, the file-variable holding a path. No
// job contains a connection value.
func TestDatabaseDeliveryTemplatesOnEveryTranslationPath(t *testing.T) {
	spec := deliverySpec()
	secrets := map[string]string{"STRIPE_KEY": "sk_test"}
	region := spec.ResolvedRegions()[0]
	service := TranslateForRegionAt(spec, "img:1", secrets, region, 5)
	for _, group := range service.TaskGroups {
		if *group.Name == "resize" {
			t.Fatal("function process was scheduled as a service task")
		}
	}
	periodic := TranslatePeriodicForRegionAt(spec, "nightly", spec.Processes["nightly"], "img:1", secrets, region, 5)
	function := TranslateBatchAt(spec, "resize", spec.Processes["resize"], "img:1", secrets, "shop-resize-1700000000000", 5)
	tasks := map[string]*nomadapi.Task{}
	jobs := map[string]string{}
	for _, job := range []*nomadapi.Job{service, periodic, function} {
		for _, group := range job.TaskGroups {
			tasks[group.Tasks[0].Name] = group.Tasks[0]
			jobs[group.Tasks[0].Name] = *job.ID
		}
		encoded, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "postgresql://") || strings.Contains(string(encoded), deliveryCanary) {
			t.Fatalf("job %s embeds a connection value", *job.ID)
		}
	}
	for _, name := range []string{"web", "worker", "nightly", "resize"} {
		task := tasks[name]
		if task == nil {
			t.Fatalf("no task %s", name)
		}
		path := DatabaseVariablePath(jobs[name])
		if len(task.Templates) != 3 {
			t.Fatalf("%s templates = %d", name, len(task.Templates))
		}
		var envTemplate *nomadapi.Template
		files := map[string]string{}
		for _, template := range task.Templates {
			data := *template.EmbeddedTmpl
			if !strings.Contains(data, `nomadVar "`+path+`"`) || strings.Count(data, "nomadVar") != 1 {
				t.Fatalf("%s template reads another variable: %q", name, data)
			}
			if *template.Perms != "0400" || *template.ChangeMode != "restart" || !*template.ErrMissingKey || !strings.HasPrefix(*template.DestPath, "secrets/") {
				t.Fatalf("%s template is not private/restarting: %+v", name, template)
			}
			if *template.Envvars {
				envTemplate = template
			} else {
				files[*template.DestPath] = data
			}
		}
		if envTemplate == nil || !strings.Contains(*envTemplate.EmbeddedTmpl, "DATABASE_URL={{ .norn_rev5_db_url_primary.Value | toJSON }}") || strings.Contains(*envTemplate.EmbeddedTmpl, "ANALYTICS") {
			t.Fatalf("%s env template = %+v", name, envTemplate)
		}
		if !strings.Contains(files["secrets/norn-databases/primary.url"], ".norn_rev5_db_url_primary") || !strings.Contains(files["secrets/norn-databases/analytics-db.url"], ".norn_rev5_db_url_analytics_db") {
			t.Fatalf("%s file templates = %v", name, files)
		}
		for _, template := range task.Templates {
			if strings.Contains(*template.EmbeddedTmpl, "{{ .norn_db_url_") {
				t.Fatalf("%s template reads mutable current items: %q", name, *template.EmbeddedTmpl)
			}
		}
		if task.Env["DATABASE_URL_FILE"] != "${NOMAD_SECRETS_DIR}/norn-databases/primary.url" || task.Env["ANALYTICS_URL_FILE"] != "${NOMAD_SECRETS_DIR}/norn-databases/analytics-db.url" {
			t.Fatalf("%s file variables = %v", name, task.Env)
		}
		if _, ok := task.Env["DATABASE_URL"]; ok {
			t.Fatalf("%s sets the value variable statically", name)
		}
		if task.Env["STRIPE_KEY"] != "sk_test" || task.Env["LOG_LEVEL"] != "info" {
			t.Fatalf("%s lost ordinary env: %v", name, task.Env)
		}
	}
	if tasks["worker"].Env["QUEUE"] != "jobs" {
		t.Fatal("per-process env lost")
	}
	if ids := DatabaseDeliveryJobIDs(spec); strings.Join(ids, ",") != "shop,shop-nightly" {
		t.Fatalf("delivery job IDs = %v", ids)
	}
	// The revision-less wrappers never fall back to mutable current items:
	// they reference revision 0, which is never written (Deliver refuses it),
	// so such a task fails closed at render.
	for _, job := range []*nomadapi.Job{Translate(spec, "img", secrets), TranslatePeriodic(spec, "nightly", spec.Processes["nightly"], "img", secrets), TranslateBatch(spec, "resize", spec.Processes["resize"], "img", secrets, "fn-1")} {
		for _, group := range job.TaskGroups {
			for _, template := range group.Tasks[0].Templates {
				if !strings.Contains(*template.EmbeddedTmpl, ".norn_rev0_db_url_") || !*template.ErrMissingKey {
					t.Fatalf("revision-less job %s template = %q", *job.ID, *template.EmbeddedTmpl)
				}
			}
		}
	}
	// A deploy's job versions read the revision staged for them, never the
	// current items that other versions render.
	staged := TranslateForRegionAt(spec, "img:2", secrets, spec.ResolvedRegions()[0], 7)
	stagedPeriodic := TranslatePeriodicForRegionAt(spec, "nightly", spec.Processes["nightly"], "img:2", secrets, spec.ResolvedRegions()[0], 7)
	for _, job := range []*nomadapi.Job{staged, stagedPeriodic} {
		for _, group := range job.TaskGroups {
			for _, template := range group.Tasks[0].Templates {
				data := *template.EmbeddedTmpl
				if !strings.Contains(data, ".norn_rev7_db_url_") || strings.Contains(data, "{{ .norn_db_url_") {
					t.Fatalf("staged job %s template = %q", *job.ID, data)
				}
			}
		}
	}
	// v1 specs are untranslated by delivery.
	legacy := &model.InfraSpec{App: "legacy", Processes: map[string]model.Process{"web": {Command: "x"}}, Infrastructure: &model.Infrastructure{Postgres: &model.PostgresInfra{Database: "legacy"}}}
	if templates := Translate(legacy, "img", nil).TaskGroups[0].Tasks[0].Templates; len(templates) != 0 {
		t.Fatalf("legacy job gained templates: %d", len(templates))
	}
}

// fakeVariables is a minimal in-memory implementation of Nomad's
// /v1/var HTTP contract (CAS via ?cas=, 409 with the current variable).
type fakeVariables struct {
	mu        sync.Mutex
	variables map[string]*nomadapi.Variable
	index     uint64
	writes    int
	regions   []string
}

func (f *fakeVariables) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1/var/")
	f.regions = append(f.regions, r.URL.Query().Get("region"))
	current := f.variables[path]
	switch r.Method {
	case http.MethodGet:
		if current == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(current)
	case http.MethodPut:
		if cas := r.URL.Query().Get("cas"); cas != "" {
			want, _ := strconv.ParseUint(cas, 10, 64)
			have := uint64(0)
			if current != nil {
				have = current.ModifyIndex
			}
			if want != have {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(current)
				return
			}
		}
		body, _ := io.ReadAll(r.Body)
		var variable nomadapi.Variable
		if err := json.Unmarshal(body, &variable); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.index++
		f.writes++
		variable.ModifyIndex = f.index
		f.variables[path] = &variable
		_ = json.NewEncoder(w).Encode(variable)
	case http.MethodDelete:
		if cas := r.URL.Query().Get("cas"); cas != "" && current != nil {
			if want, _ := strconv.ParseUint(cas, 10, 64); want != current.ModifyIndex {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(current)
				return
			}
		}
		delete(f.variables, path)
		w.WriteHeader(http.StatusNoContent)
	}
}

func newFakeVariableClient(t *testing.T) (*Client, *fakeVariables) {
	t.Helper()
	fake := &fakeVariables{variables: map[string]*nomadapi.Variable{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client, fake
}

func TestDatabaseDeliveryRejectsOversizedStagedVariableBeforeWrite(t *testing.T) {
	client, fake := newFakeVariableClient(t)
	secret := strings.Repeat("x", maxDatabaseVariableItemBytes/2)
	items := map[string]string{DatabaseComponentItemKey("primary", "password"): secret}
	err := client.DeliverDatabaseVariable("global", "shop", items, 7)
	if !errors.Is(err, ErrDatabaseVariableTooLarge) || strings.Contains(err.Error(), secret) || fake.writes != 0 {
		t.Fatalf("oversized initial delivery: err=%v writes=%d", err, fake.writes)
	}
	if err := client.DeliverDatabaseVariable("global", "shop", map[string]string{DatabaseComponentItemKey("primary", "password"): strings.Repeat("s", 30_000)}, 7); err != nil {
		t.Fatal(err)
	}
	before := fake.writes
	err = client.DeliverDatabaseVariable("global", "shop", map[string]string{DatabaseComponentItemKey("primary", "password"): strings.Repeat("y", 6_000)}, 8)
	if !errors.Is(err, ErrDatabaseVariableTooLarge) || fake.writes != before {
		t.Fatalf("oversized second revision: err=%v writes=%d", err, fake.writes)
	}
}

func TestDatabaseVariableWritesAreCheckedIdempotentAndRedacted(t *testing.T) {
	client, fake := newFakeVariableClient(t)
	items := DatabaseVariableItems(map[string]string{"primary": "postgresql://app:" + deliveryCanary + "@db:5432/shop?sslmode=disable"})
	if err := client.DeliverDatabaseVariable("west", "shop", items, 3); err != nil {
		t.Fatal(err)
	}
	if err := client.DeliverDatabaseVariable("west", "shop", items, 3); err != nil || fake.writes != 1 {
		t.Fatalf("unchanged value rewritten: writes=%d err=%v", fake.writes, err)
	}
	if fake.regions[0] != "west" {
		t.Fatalf("region not forwarded: %v", fake.regions)
	}
	stored := func(key string) string {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.variables["nomad/jobs/shop"].Items[key]
	}
	primary, primaryAt := DatabaseItemKey("primary"), func(revision int64) string { return DatabaseRevisionItemKey("primary", revision) }
	// The first delivery (nothing rendering yet) is current and staged.
	if stored(primary) != items[primary] || stored(primaryAt(3)) != items[primary] {
		t.Fatal("initial delivery was not published")
	}
	changed := DatabaseVariableItems(map[string]string{"primary": "postgresql://app:rotated@db:5432/shop?sslmode=disable"})
	// Same revision, different values: never an implicit rotation.
	if err := client.DeliverDatabaseVariable("west", "shop", changed, 3); !errors.Is(err, ErrDatabaseVariableConflict) || fake.writes != 1 {
		t.Fatalf("same-revision change = writes %d, %v", fake.writes, err)
	}
	// A plain put never replaces different values either.
	if err := client.PutDatabaseVariable("west", "shop", changed); !errors.Is(err, ErrDatabaseVariableConflict) || fake.writes != 1 {
		t.Fatalf("unfenced replacement = writes %d, %v", fake.writes, err)
	}
	// A newer revision is staged without touching the current items that
	// running allocations render; promotion cuts over.
	if err := client.DeliverDatabaseVariable("west", "shop", changed, 4); err != nil || fake.writes != 2 {
		t.Fatalf("newer revision = writes %d, %v", fake.writes, err)
	}
	if stored(primary) != items[primary] || stored(primaryAt(4)) != changed[primary] {
		t.Fatal("staging changed current material")
	}
	// Staging another revision leaves the bytes an older revision's
	// allocations render untouched (template output unchanged → no restart;
	// the restart itself is agent behaviour and unqualified here).
	if stored(primaryAt(3)) != items[primary] {
		t.Fatal("staging revision 4 altered revision 3 material")
	}
	if revision, err := client.PromotedDatabaseRevision("west", "shop"); err != nil || revision != 3 {
		t.Fatalf("promoted before cutover = %d, %v", revision, err)
	}
	if err := client.PromoteDatabaseVariable("west", "shop", 4); err != nil || fake.writes != 3 || stored(primary) != changed[primary] {
		t.Fatalf("promotion = writes %d, %v", fake.writes, err)
	}
	if err := client.PromoteDatabaseVariable("west", "shop", 4); err != nil || fake.writes != 3 {
		t.Fatalf("repeated promotion = writes %d, %v", fake.writes, err)
	}
	// Older revisions are now stale for both staging and promotion.
	if err := client.DeliverDatabaseVariable("west", "shop", items, 3); !errors.Is(err, ErrStaleDatabaseDelivery) || fake.writes != 3 || strings.Contains(err.Error(), deliveryCanary) {
		t.Fatalf("older revision = writes %d, %v", fake.writes, err)
	}
	if err := client.PromoteDatabaseVariable("west", "shop", 3); !errors.Is(err, ErrStaleDatabaseDelivery) {
		t.Fatalf("older promotion = %v", err)
	}
	if err := client.PromoteDatabaseVariable("west", "shop", 7); !errors.Is(err, ErrDatabaseVariableConflict) {
		t.Fatalf("unstaged promotion = %v", err)
	}
	if err := client.DeliverDatabaseVariable("west", "shop", items, 0); err == nil {
		t.Fatal("unfenced delivery accepted")
	}
	// Promotion keeps the previous promoted revision (allocations still
	// draining) and prunes older staged material.
	if stored(primaryAt(3)) == "" {
		t.Fatal("previous revision pruned while it may still be draining")
	}
	if err := client.DeliverDatabaseVariable("west", "shop", items, 5); err != nil {
		t.Fatal(err)
	}
	if err := client.PromoteDatabaseVariable("west", "shop", 5); err != nil {
		t.Fatal(err)
	}
	if stored(primaryAt(3)) != "" || stored(primaryAt(4)) == "" || stored(primaryAt(5)) == "" {
		t.Fatal("pruning kept the wrong revisions")
	}
	// A concurrent publisher between the fence read and the write turns the
	// write into a conflict, not an overwrite.
	fake.mu.Lock()
	fake.variables["nomad/jobs/shop"].ModifyIndex += 100
	fake.mu.Unlock()
	racing := &casRaceClient{fake: fake}
	server := httptest.NewServer(racing)
	defer server.Close()
	raceClient, _ := NewClient(server.URL)
	if err := raceClient.DeliverDatabaseVariable("west", "shop", items, 9); !errors.Is(err, ErrDatabaseVariableConflict) || strings.Contains(err.Error(), deliveryCanary) {
		t.Fatalf("racing write = %v", err)
	}
	// Target identities are staged with the values, per revision.
	withTarget := DatabaseVariableItems(map[string]string{"primary": "postgresql://app:t@db:5432/shop?sslmode=disable"})
	withTarget[DatabaseTargetItemKey("primary")] = `{"bindingId":"shop-primary","bindingGeneration":1}`
	if err := client.DeliverDatabaseVariable("west", "shop", withTarget, 10); err != nil {
		t.Fatal(err)
	}
	revision10, err := client.ReadDatabaseRevision("west", "shop", 10)
	if err != nil || revision10.URLs["primary"] != withTarget[DatabaseItemKey("primary")] || revision10.Targets["primary"] != withTarget[DatabaseTargetItemKey("primary")] || revision10.Promoted != 5 {
		t.Fatalf("revision 10 = %+v, %v", revision10, err)
	}
	// revision 1 must not be confused with revision 10 (exact revision parse).
	if revision1, err := client.ReadDatabaseRevision("west", "shop", 1); err != nil || len(revision1.URLs) != 0 {
		t.Fatalf("revision 1 = %+v, %v", revision1, err)
	}
	// A function invocation gets its own create-only copy of exactly one
	// revision, then deletes only that exact material.
	owned, err := client.CopyDatabaseVariable("west", "shop-resize-1", revision10)
	if err != nil {
		t.Fatal(err)
	}
	got := fake.variables["nomad/jobs/shop-resize-1"]
	if got == nil || got.Items[DatabaseRevisionItemKey("primary", 10)] != revision10.URLs["primary"] || len(got.Items) != 3 {
		t.Fatalf("copied variable = %+v", got)
	}
	if _, err := client.CopyDatabaseVariable("west", "shop-resize-1", revision10); err != nil {
		t.Fatalf("identical re-copy = %v", err)
	}
	other := revision10
	other.URLs = map[string]string{"primary": "postgresql://elsewhere"}
	if _, err := client.CopyDatabaseVariable("west", "shop-resize-1", other); !errors.Is(err, ErrDatabaseVariableConflict) {
		t.Fatalf("copy over an existing invocation variable = %v", err)
	}
	if err := client.DeleteDatabaseVariable("west", "shop-resize-1", map[string]string{"x": "y"}); !errors.Is(err, ErrDatabaseVariableConflict) || fake.variables["nomad/jobs/shop-resize-1"] == nil {
		t.Fatalf("delete of material not owned = %v", err)
	}
	if err := client.DeleteDatabaseVariable("west", "shop-resize-1", owned); err != nil || fake.variables["nomad/jobs/shop-resize-1"] != nil {
		t.Fatalf("delete = %v", err)
	}
	if _, err := client.CopyDatabaseVariable("west", "undeployed-fn-1", DatabaseRevision{}); err == nil {
		t.Fatal("copy of an empty revision succeeded")
	}
	// Transport errors never echo item values.
	broken, _ := NewClient("http://127.0.0.1:1")
	if err := broken.PutDatabaseVariable("west", "shop", items); err == nil || strings.Contains(err.Error(), deliveryCanary) {
		t.Fatalf("broken transport = %v", err)
	}
}

// casRaceClient serves reads with a stale index so the following checked
// write races a concurrent writer.
type casRaceClient struct{ fake *fakeVariables }

func (c *casRaceClient) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		c.fake.mu.Lock()
		stale := *c.fake.variables[strings.TrimPrefix(r.URL.Path, "/v1/var/")]
		c.fake.mu.Unlock()
		stale.ModifyIndex -= 100
		stale.Items = nomadapi.VariableItems{"other": "value", deliveryRevisionItem: "1"}
		_ = json.NewEncoder(w).Encode(stale)
		return
	}
	c.fake.ServeHTTP(w, r)
}
