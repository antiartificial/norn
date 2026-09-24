package nomad

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

// Run with a disposable Nomad agent and Docker driver:
// NORN_TEST_NOMAD_ADDR=http://127.0.0.1:14646 go test ./nomad -run TestGeneratedWordPressDatabaseDeliveryInNomad -count=1 -v
// The test creates uniquely named jobs and variables and removes them on exit.
func TestGeneratedWordPressDatabaseDeliveryInNomad(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	if address == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR to run allocation delivery qualification")
	}
	client, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	app := fmt.Sprintf("norn-m2-delivery-%d", time.Now().UnixNano())
	password := `space "quote" back\slash #hash single'quote`
	want := sha256.Sum256([]byte(password))
	wantHex := hex.EncodeToString(want[:])
	command := `printf '%s' "$WORDPRESS_DB_PASSWORD" | sha256sum`
	spec := &model.InfraSpec{SchemaVersion: model.AppSchemaV2, App: app,
		Processes: map[string]model.Process{
			"web":  {Command: command + "; sleep 60"},
			"cron": {Command: command, Schedule: "@hourly"},
			"fn":   {Command: command, Function: &model.FunctionSpec{Timeout: "30s"}},
		},
		Databases: []model.DatabaseRequirement{{Name: "primary", Purpose: "application", Capabilities: []string{"runtime"}, Runtime: &model.DatabaseRuntime{
			Components: &model.DatabaseRuntimeComponents{Host: "WORDPRESS_DB_HOST", User: "WORDPRESS_DB_USER", Password: "WORDPRESS_DB_PASSWORD", Name: "WORDPRESS_DB_NAME"},
		}}},
	}
	items := map[string]string{
		DatabaseComponentItemKey("primary", "host"):     "mysql.internal:3306",
		DatabaseComponentItemKey("primary", "user"):     "wp",
		DatabaseComponentItemKey("primary", "password"): password,
		DatabaseComponentItemKey("primary", "name"):     "wordpress",
	}
	region := spec.ResolvedRegions()[0]
	service := TranslateForRegionAt(spec, "wordpress:latest", nil, region, 7)
	periodic := TranslatePeriodicForRegionAt(spec, "cron", spec.Processes["cron"], "wordpress:latest", nil, region, 7)
	functionID := app + "-fn-1"
	function := TranslateBatchAt(spec, "fn", spec.Processes["fn"], "wordpress:latest", nil, functionID, 7)
	for _, job := range []*nomadapi.Job{service, periodic, function} {
		jobID := *job.ID
		// Keep the generated Docker task, templates, and variable paths intact.
		// The test uses the local image and allocates no network port.
		for _, group := range job.TaskGroups {
			for _, task := range group.Tasks {
				task.Config["force_pull"] = false
			}
		}
		t.Cleanup(func() {
			_, _, _ = client.api.Jobs().Deregister(jobID, true, nil)
			_, _ = client.api.Variables().Delete(DatabaseVariablePath(jobID), nil)
		})
	}
	for _, jobID := range []string{*service.ID, *periodic.ID} {
		if err := client.DeliverDatabaseVariable(region.NomadRegion, jobID, items, 7); err != nil {
			t.Fatal(err)
		}
	}
	for _, job := range []*nomadapi.Job{service, periodic} {
		if _, _, err := client.api.Jobs().Register(job, nil); err != nil {
			t.Fatalf("register %s: %v", *job.ID, err)
		}
	}
	checkAllocationOutput(t, client.api, *service.ID, "web", wantHex)
	if _, _, err := client.api.Jobs().PeriodicForce(*periodic.ID, nil); err != nil {
		t.Fatalf("force periodic: %v", err)
	}
	checkPeriodicDigest(t, client.api, *periodic.ID, "cron", wantHex)
	revision, err := client.ReadDatabaseRevision(region.NomadRegion, *service.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CopyDatabaseVariable(region.NomadRegion, functionID, revision); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.api.Jobs().Register(function, nil); err != nil {
		t.Fatalf("register function: %v", err)
	}
	checkAllocationOutput(t, client.api, functionID, "fn", wantHex)
}

// This separate opt-in probe uses a disposable MySQL target reachable from a
// Nomad Docker allocation. It verifies that the WordPress image consumes the
// delivered components for identity, write, and read operations.
func TestGeneratedWordPressMySQLRuntimeInNomad(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	host, user, password, name := os.Getenv("NORN_TEST_MYSQL_ALLOCATION_HOST"), os.Getenv("NORN_TEST_MYSQL_USER"), os.Getenv("NORN_TEST_MYSQL_PASSWORD"), os.Getenv("NORN_TEST_MYSQL_DATABASE")
	if address == "" || host == "" || user == "" || password == "" || name == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR and NORN_TEST_MYSQL_{ALLOCATION_HOST,USER,PASSWORD,DATABASE} for disposable MySQL allocation qualification")
	}
	client, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	app := fmt.Sprintf("norn-m2-mysql-%d", time.Now().UnixNano())
	// All SQL state is in a temporary table, which disappears with this one
	// connection. The command prints a fixed marker and never a credential.
	const command = `php -r '$m=new mysqli(getenv("WORDPRESS_DB_HOST"),getenv("WORDPRESS_DB_USER"),getenv("WORDPRESS_DB_PASSWORD"),getenv("WORDPRESS_DB_NAME")); if($m->connect_errno){exit(2);} $identity=$m->query("SELECT DATABASE() AS db, SUBSTRING_INDEX(CURRENT_USER(), 0x40, 1) AS role"); if(!$identity){exit(3);} $row=$identity->fetch_assoc(); if($row["db"]!==getenv("WORDPRESS_DB_NAME") || $row["role"]!==getenv("WORDPRESS_DB_USER")){exit(4);} if(!$m->query("CREATE TEMPORARY TABLE norn_qual (value VARCHAR(32))")){exit(5);} if(!$m->query("INSERT INTO norn_qual VALUES (0x6f6b)")){exit(6);} $read=$m->query("SELECT value FROM norn_qual"); if(!$read || $read->fetch_row()[0]!=="ok"){exit(7);} echo "mysql-runtime-ok\n";'`
	spec := &model.InfraSpec{SchemaVersion: model.AppSchemaV2, App: app,
		Processes: map[string]model.Process{"web": {Command: command + " && sleep 30"}},
		Databases: []model.DatabaseRequirement{{Name: "primary", Purpose: "application", Capabilities: []string{"runtime"}, Runtime: &model.DatabaseRuntime{
			Components: &model.DatabaseRuntimeComponents{Host: "WORDPRESS_DB_HOST", User: "WORDPRESS_DB_USER", Password: "WORDPRESS_DB_PASSWORD", Name: "WORDPRESS_DB_NAME"},
		}}},
	}
	job := TranslateForRegionAt(spec, "wordpress:latest", nil, spec.ResolvedRegions()[0], 7)
	jobID := *job.ID
	t.Cleanup(func() {
		_, _, _ = client.api.Jobs().Deregister(jobID, true, nil)
		_, _ = client.api.Variables().Delete(DatabaseVariablePath(jobID), nil)
	})
	items := map[string]string{
		DatabaseComponentItemKey("primary", "host"): host, DatabaseComponentItemKey("primary", "user"): user,
		DatabaseComponentItemKey("primary", "password"): password, DatabaseComponentItemKey("primary", "name"): name,
	}
	if err := client.DeliverDatabaseVariable("global", jobID, items, 7); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.api.Jobs().Register(job, nil); err != nil {
		t.Fatal(err)
	}
	checkAllocationOutput(t, client.api, jobID, "web", "mysql-runtime-ok")
}

func checkPeriodicDigest(t *testing.T, api *nomadapi.Client, parentID, task, want string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _, err := api.Jobs().List(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range jobs {
			if strings.HasPrefix(job.ID, parentID+"/periodic-") {
				childID := job.ID
				t.Cleanup(func() { _, _, _ = api.Jobs().Deregister(childID, true, nil) })
				checkAllocationOutput(t, api, job.ID, task, want)
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("periodic child of %s was not registered", parentID)
}

func checkAllocationOutput(t *testing.T, api *nomadapi.Client, jobID, task, want string) {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		allocs, _, err := api.Jobs().Allocations(jobID, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, stub := range allocs {
			if stub.ClientStatus == "failed" || stub.ClientStatus == "lost" {
				t.Fatalf("%s allocation %s: %s", jobID, stub.ID, stub.ClientDescription)
			}
			if stub.ClientStatus != "running" && stub.ClientStatus != "complete" {
				continue
			}
			alloc, _, err := api.Allocations().Info(stub.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			frames, errs := api.AllocFS().Logs(alloc, false, task, "stdout", "start", 0, nil, nil)
			var output strings.Builder
			for frame := range frames {
				output.Write(frame.Data)
			}
			select {
			case err := <-errs:
				if err != nil {
					t.Fatal(err)
				}
			default:
			}
			if strings.Contains(output.String(), want) {
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s allocation never reported the expected output marker", jobID)
}
