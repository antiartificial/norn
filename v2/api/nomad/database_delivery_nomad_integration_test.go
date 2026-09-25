package nomad

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
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

// TestGeneratedWordPressMySQLTLSRuntimeInNomad is the opt-in allocation
// qualification for the dormant MySQL TLS file-delivery contract. It proves
// that Nomad renders the CA and optional client PEM files below the allocation
// secrets directory, mysqli consumes those paths, and the connection actually
// negotiated TLS. It deliberately does not change database's resolver gate:
// that gate can move only after this test is run successfully against the
// exact supported image and disposable verified-TLS MySQL target.
//
// Set NORN_TEST_NOMAD_ADDR, NORN_TEST_MYSQL_{ALLOCATION_HOST,USER,PASSWORD,
// DATABASE}, and NORN_TEST_MYSQL_TLS_CA_PEM_B64. If the server requires a
// client certificate, also set NORN_TEST_MYSQL_TLS_{CLIENT_CERT,CLIENT_KEY}_B64.
func TestGeneratedWordPressMySQLTLSRuntimeInNomad(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	host, user, password, name := os.Getenv("NORN_TEST_MYSQL_ALLOCATION_HOST"), os.Getenv("NORN_TEST_MYSQL_USER"), os.Getenv("NORN_TEST_MYSQL_PASSWORD"), os.Getenv("NORN_TEST_MYSQL_DATABASE")
	ca, ok := qualificationPEM(t, "NORN_TEST_MYSQL_TLS_CA_PEM_B64")
	if address == "" || host == "" || user == "" || password == "" || name == "" || !ok {
		t.Skip("set Nomad, disposable MySQL, and base64 CA variables to run verified TLS allocation qualification")
	}
	cert, hasCert := qualificationPEM(t, "NORN_TEST_MYSQL_TLS_CLIENT_CERT_B64")
	key, hasKey := qualificationPEM(t, "NORN_TEST_MYSQL_TLS_CLIENT_KEY_B64")
	if hasCert != hasKey {
		t.Fatal("NORN_TEST_MYSQL_TLS_CLIENT_CERT_B64 and NORN_TEST_MYSQL_TLS_CLIENT_KEY_B64 must be set together")
	}
	client, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	app := fmt.Sprintf("norn-m2-mysql-tls-%d", time.Now().UnixNano())
	const command = `php -r '$m=mysqli_init(); mysqli_options($m, MYSQLI_OPT_SSL_VERIFY_SERVER_CERT, true); mysqli_ssl_set($m, getenv("MYSQL_SSL_KEY") ?: null, getenv("MYSQL_SSL_CERT") ?: null, getenv("MYSQL_SSL_CA"), null, null); if(!mysqli_real_connect($m, getenv("WORDPRESS_DB_HOST"), getenv("WORDPRESS_DB_USER"), getenv("WORDPRESS_DB_PASSWORD"), getenv("WORDPRESS_DB_NAME"), null, null, MYSQLI_CLIENT_SSL)){exit(2);} $q=$m->query("SHOW SESSION STATUS WHERE Variable_name = 0x53736c5f636970686572"); if(!$q || !($r=$q->fetch_row()) || $r[1]===""){exit(3);} $identity=$m->query("SELECT DATABASE() AS db, SUBSTRING_INDEX(CURRENT_USER(), 0x40, 1) AS role"); if(!$identity || !($row=$identity->fetch_assoc()) || $row["db"]!==getenv("WORDPRESS_DB_NAME") || $row["role"]!==getenv("WORDPRESS_DB_USER")){exit(4);} echo "mysql-tls-runtime-ok\\n";'`
	runtimeTLS := &model.DatabaseRuntimeTLS{CAFileEnv: "MYSQL_SSL_CA"}
	if hasCert {
		runtimeTLS.ClientCertFileEnv, runtimeTLS.ClientKeyFileEnv = "MYSQL_SSL_CERT", "MYSQL_SSL_KEY"
	}
	spec := &model.InfraSpec{SchemaVersion: model.AppSchemaV2, App: app,
		Processes: map[string]model.Process{"web": {Command: command + " && sleep 30"}},
		Databases: []model.DatabaseRequirement{{Name: "primary", Purpose: "application", Capabilities: []string{"runtime"}, Runtime: &model.DatabaseRuntime{
			Components: &model.DatabaseRuntimeComponents{Host: "WORDPRESS_DB_HOST", User: "WORDPRESS_DB_USER", Password: "WORDPRESS_DB_PASSWORD", Name: "WORDPRESS_DB_NAME"}, TLS: runtimeTLS,
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
		DatabaseTLSItemKey("primary", "ca"): string(ca),
	}
	if hasCert {
		items[DatabaseTLSItemKey("primary", "client_cert")] = string(cert)
		items[DatabaseTLSItemKey("primary", "client_key")] = string(key)
	}
	region := spec.ResolvedRegions()[0]
	if err := client.DeliverDatabaseVariable(region.NomadRegion, jobID, items, 7); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.api.Jobs().Register(job, nil); err != nil {
		t.Fatal(err)
	}
	checkAllocationOutput(t, client.api, jobID, "web", "mysql-tls-runtime-ok")
}

// TestStockWordPressMySQLTLSStartupInNomad boots the official WordPress
// Apache image with its normal entrypoint and wp-config-docker.php. The
// supported WORDPRESS_CONFIG_EXTRA hook selects MYSQLI_CLIENT_SSL;
// SSL_CERT_FILE points at Norn's private CA template. This proves encryption
// only: a separate wrong-CA control demonstrates that these stock hooks do
// not enforce CA verification. A shutdown observer reports only whether
// WordPress's own wpdb connection negotiated a cipher.
//
// Run against a disposable TLS MySQL target with the variables above plus
// NORN_TEST_WORDPRESS_HOST_PORT (a free local host port, e.g. 18080).
func TestStockWordPressMySQLTLSStartupInNomad(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	host, user, password, name := os.Getenv("NORN_TEST_MYSQL_ALLOCATION_HOST"), os.Getenv("NORN_TEST_MYSQL_USER"), os.Getenv("NORN_TEST_MYSQL_PASSWORD"), os.Getenv("NORN_TEST_MYSQL_DATABASE")
	ca, hasCA := qualificationPEM(t, "NORN_TEST_MYSQL_TLS_CA_PEM_B64")
	port, err := strconv.Atoi(os.Getenv("NORN_TEST_WORDPRESS_HOST_PORT"))
	if address == "" || host == "" || user == "" || password == "" || name == "" || !hasCA || err != nil || port < 1 || port > 65535 {
		t.Skip("set disposable Nomad/MySQL TLS variables and NORN_TEST_WORDPRESS_HOST_PORT for stock WordPress startup qualification")
	}
	client, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	jobID := registerStockWordPressTLSJob(t, client, host, user, password, name, ca, port)
	pageURL := fmt.Sprintf("http://127.0.0.1:%d/wp-admin/install.php", port)
	httpClient := &http.Client{Timeout: 5 * time.Second}
	var allocation *nomadapi.Allocation
	pageReady := false
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		allocs, _, err := client.api.Jobs().Allocations(jobID, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, stub := range allocs {
			if stub.ClientStatus == "failed" || stub.ClientStatus == "lost" {
				t.Fatalf("stock WordPress allocation %s: %s", stub.ID, stub.ClientDescription)
			}
			if stub.ClientStatus == "running" {
				allocation, _, err = client.api.Allocations().Info(stub.ID, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		if allocation != nil {
			response, err := httpClient.Get(pageURL)
			if err == nil {
				body, readErr := io.ReadAll(io.LimitReader(response.Body, 256<<10))
				_ = response.Body.Close()
				if readErr == nil && response.StatusCode == http.StatusOK && strings.Contains(string(body), "WordPress") {
					pageReady = true
					break
				}
			}
		}
		time.Sleep(time.Second)
	}
	if !pageReady || allocation == nil {
		t.Fatal("stock WordPress installation page did not become ready over HTTP")
	}
	// The observer queries the same mysqli handle WordPress used to render
	// the page; no connection material or cipher name is logged.
	for attempt := 0; attempt < 10; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		frames, errs := client.api.AllocFS().Logs(allocation, false, "web", "stderr", "start", 0, ctx.Done(), nil)
		var output strings.Builder
		for frame := range frames {
			output.Write(frame.Data)
		}
		cancel()
		select {
		case err := <-errs:
			if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
				t.Fatalf("read WordPress allocation logs: %v", err)
			}
		default:
		}
		if strings.Contains(output.String(), "norn-stock-tls:cipher-nonempty") {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("stock WordPress served HTTP but its wpdb session did not report a negotiated TLS cipher")
}

const stockWordPressTLSConfigExtra = `define('MYSQL_CLIENT_FLAGS', MYSQLI_CLIENT_SSL);
register_shutdown_function(function () {
    global $wpdb;
    if (!isset($wpdb->dbh) || !($wpdb->dbh instanceof mysqli)) { error_log('norn-stock-tls:no-db-handle'); return; }
    $result = $wpdb->dbh->query('SHOW SESSION STATUS WHERE Variable_name = 0x53736c5f636970686572');
    if (!$result || !($row = $result->fetch_row())) { error_log('norn-stock-tls:no-cipher-status'); return; }
    error_log('norn-stock-tls:cipher-' . ($row[1] === '' ? 'empty' : 'nonempty'));
});`

func registerStockWordPressTLSJob(t *testing.T, client *Client, host, user, password, name string, ca []byte, port int) string {
	t.Helper()
	app := fmt.Sprintf("norn-stock-wordpress-tls-%d", time.Now().UnixNano())
	spec := &model.InfraSpec{SchemaVersion: model.AppSchemaV2, App: app,
		Env:       map[string]string{"WORDPRESS_CONFIG_EXTRA": stockWordPressTLSConfigExtra},
		Processes: map[string]model.Process{"web": {Port: 80, HostPort: port, Resources: &model.Resources{CPU: 500, Memory: 512}}},
		Databases: []model.DatabaseRequirement{{Name: "primary", Purpose: "application", Capabilities: []string{"runtime"}, Runtime: &model.DatabaseRuntime{
			Components: &model.DatabaseRuntimeComponents{Host: "WORDPRESS_DB_HOST", User: "WORDPRESS_DB_USER", Password: "WORDPRESS_DB_PASSWORD", Name: "WORDPRESS_DB_NAME"},
			TLS:        &model.DatabaseRuntimeTLS{CAFileEnv: "SSL_CERT_FILE"},
		}}},
	}
	region := spec.ResolvedRegions()[0]
	job := TranslateForRegionAt(spec, "wordpress:6.8.2-php8.3-apache", nil, region, 7)
	jobID := *job.ID
	// This disposable local agent has no Consul server. Keep the generated
	// Docker networking and private database templates intact.
	job.TaskGroups[0].Services = nil
	job.TaskGroups[0].Tasks[0].Config["force_pull"] = false
	t.Cleanup(func() {
		_, _, _ = client.api.Jobs().Deregister(jobID, true, nil)
		_, _ = client.api.Variables().Delete(DatabaseVariablePath(jobID), nil)
	})
	items := map[string]string{
		DatabaseComponentItemKey("primary", "host"): host, DatabaseComponentItemKey("primary", "user"): user,
		DatabaseComponentItemKey("primary", "password"): password, DatabaseComponentItemKey("primary", "name"): name,
		DatabaseTLSItemKey("primary", "ca"): string(ca),
	}
	if err := client.DeliverDatabaseVariable(region.NomadRegion, jobID, items, 7); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.api.Jobs().Register(job, nil); err != nil {
		t.Fatal(err)
	}
	return jobID
}

// TestStockWordPressRejectsWrongCAInNomad is an opt-in release gate, expected
// to fail for the current stock WordPress MYSQL_CLIENT_FLAGS configuration.
// An unrelated CA must prevent WordPress from opening its own database
// connection; a nonempty TLS cipher alone is insufficient evidence.
// Set NORN_TEST_MYSQL_TLS_WRONG_CA_PEM_B64 and a separate free
// NORN_TEST_WORDPRESS_WRONG_CA_HOST_PORT in addition to the normal fixture.
func TestStockWordPressRejectsWrongCAInNomad(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	host, user, password, name := os.Getenv("NORN_TEST_MYSQL_ALLOCATION_HOST"), os.Getenv("NORN_TEST_MYSQL_USER"), os.Getenv("NORN_TEST_MYSQL_PASSWORD"), os.Getenv("NORN_TEST_MYSQL_DATABASE")
	wrongCA, hasWrongCA := qualificationPEM(t, "NORN_TEST_MYSQL_TLS_WRONG_CA_PEM_B64")
	port, err := strconv.Atoi(os.Getenv("NORN_TEST_WORDPRESS_WRONG_CA_HOST_PORT"))
	if address == "" || host == "" || user == "" || password == "" || name == "" || !hasWrongCA || err != nil || port < 1 || port > 65535 {
		t.Skip("set disposable Nomad/MySQL TLS variables, unrelated CA, and a separate WordPress host port")
	}
	client, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	jobID := registerStockWordPressTLSJob(t, client, host, user, password, name, wrongCA, port)
	pageURL := fmt.Sprintf("http://127.0.0.1:%d/wp-admin/install.php", port)
	httpClient := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		allocs, _, err := client.api.Jobs().Allocations(jobID, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, stub := range allocs {
			if stub.ClientStatus == "failed" || stub.ClientStatus == "lost" {
				t.Fatalf("wrong-CA WordPress allocation %s: %s", stub.ID, stub.ClientDescription)
			}
			if stub.ClientStatus != "running" {
				continue
			}
			response, err := httpClient.Get(pageURL)
			if err != nil {
				continue
			}
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 256<<10))
			_ = response.Body.Close()
			if readErr != nil {
				continue
			}
			if response.StatusCode == http.StatusOK && strings.Contains(string(body), "WordPress") && !strings.Contains(string(body), "Error establishing a database connection") {
				t.Fatal("stock WordPress accepted an unrelated MySQL CA and served its installation page; verified TLS runtime must remain gated")
			}
			if strings.Contains(string(body), "Error establishing a database connection") {
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatal("wrong-CA WordPress did not give decisive database connection evidence")
}

func qualificationPEM(t *testing.T, name string) ([]byte, bool) {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return nil, false
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(decoded) == 0 || len(decoded) > maxDatabaseVariableItemBytes {
		t.Fatalf("%s must be a non-empty base64 PEM within the Nomad item limit", name)
	}
	return decoded, true
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
