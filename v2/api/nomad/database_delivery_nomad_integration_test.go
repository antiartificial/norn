package nomad

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	return registerStockWordPressTLSJobWithMutation(t, client, host, user, password, name, ca, port, nil)
}

// registerStockWordPressTLSJobWithMutation changes only this disposable test
// job before its first registration. Production translation remains untouched.
func registerStockWordPressTLSJobWithMutation(t *testing.T, client *Client, host, user, password, name string, ca []byte, port int, mutate func(*nomadapi.Job)) string {
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
	if mutate != nil {
		mutate(job)
	}
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

// TestWordPressVerifiedTLSDropInInNomad qualifies the supported WordPress
// db.php extension hook against a disposable TLS MySQL server. The positive
// allocation reaches the real installation page. Both an unrelated CA and a
// trusted-CA hostname mismatch must render WordPress's database-connection
// failure page over HTTP. The adapter is deliberately a qualification
// artifact: database's resolver gate remains closed until a production
// InfraSpec-to-startup wiring is reviewed.
func TestWordPressVerifiedTLSDropInInNomad(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	host, user, password, name := os.Getenv("NORN_TEST_MYSQL_ALLOCATION_HOST"), os.Getenv("NORN_TEST_MYSQL_USER"), os.Getenv("NORN_TEST_MYSQL_PASSWORD"), os.Getenv("NORN_TEST_MYSQL_DATABASE")
	ca, hasCA := qualificationPEM(t, "NORN_TEST_MYSQL_TLS_CA_PEM_B64")
	wrongCA, hasWrongCA := qualificationPEM(t, "NORN_TEST_MYSQL_TLS_WRONG_CA_PEM_B64")
	wrongHost := os.Getenv("NORN_TEST_MYSQL_TLS_WRONG_HOST")
	goodPort, goodPortErr := strconv.Atoi(os.Getenv("NORN_TEST_WORDPRESS_HOST_PORT"))
	wrongCAPort, wrongCAPortErr := strconv.Atoi(os.Getenv("NORN_TEST_WORDPRESS_WRONG_CA_HOST_PORT"))
	wrongHostPort, wrongHostPortErr := strconv.Atoi(os.Getenv("NORN_TEST_WORDPRESS_WRONG_HOST_HOST_PORT"))
	if address == "" || host == "" || wrongHost == "" || user == "" || password == "" || name == "" || !hasCA || !hasWrongCA || goodPortErr != nil || wrongCAPortErr != nil || wrongHostPortErr != nil || goodPort < 1 || wrongCAPort < 1 || wrongHostPort < 1 || goodPort > 65535 || wrongCAPort > 65535 || wrongHostPort > 65535 || goodPort == wrongCAPort || goodPort == wrongHostPort || wrongCAPort == wrongHostPort {
		t.Skip("set disposable Nomad/MySQL TLS variables, both CA values, a trusted-CA wrong host, and three WordPress host ports for db.php qualification")
	}
	client, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	goodJob := registerWordPressVerifiedTLSDropInJob(t, client, host, user, password, name, ca, goodPort)
	assertWordPressDatabasePage(t, client, goodJob, goodPort, true, "verified")
	wrongCAJob := registerWordPressVerifiedTLSDropInJob(t, client, host, user, password, name, wrongCA, wrongCAPort)
	assertWordPressDatabasePage(t, client, wrongCAJob, wrongCAPort, false, "wrong-CA")
	wrongHostJob := registerWordPressVerifiedTLSDropInJob(t, client, wrongHost, user, password, name, ca, wrongHostPort)
	assertWordPressDatabasePage(t, client, wrongHostJob, wrongHostPort, false, "hostname-mismatch")
}

// TestWordPressVerifiedTLSStartupAdapterPersistentContentInNomad exercises the
// production Translate path, including the exact qualified image and startup
// adapter. The fixture must provide a writable Nomad host volume and a
// pre-existing sentinel within that volume. The same sentinel must be served
// from wp-content before and after a replacement allocation; this makes the
// host-volume persistence claim observable instead of relying on the job spec.
//
// In addition to the normal disposable Nomad/MySQL TLS variables, set
// NORN_TEST_WORDPRESS_HOST_PORT, NORN_TEST_WORDPRESS_WRONG_CA_HOST_PORT,
// NORN_TEST_WORDPRESS_WRONG_HOST_HOST_PORT, NORN_TEST_MYSQL_TLS_WRONG_HOST,
// NORN_TEST_WORDPRESS_CONTENT_HOST_VOLUME, and
// NORN_TEST_WORDPRESS_CONTENT_SENTINEL_PATH. The sentinel path must be an
// absolute regular file under the host volume and is read without modification.
// NORN_TEST_MYSQL_TLS_WRONG_HOST_DOCKER_EXTRA_HOST may supply a Docker host
// alias such as wrong-wp-host:host-gateway for a reachable wrong hostname.
func TestWordPressVerifiedTLSStartupAdapterPersistentContentInNomad(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	host, user, password, name := os.Getenv("NORN_TEST_MYSQL_ALLOCATION_HOST"), os.Getenv("NORN_TEST_MYSQL_USER"), os.Getenv("NORN_TEST_MYSQL_PASSWORD"), os.Getenv("NORN_TEST_MYSQL_DATABASE")
	ca, hasCA := qualificationPEM(t, "NORN_TEST_MYSQL_TLS_CA_PEM_B64")
	wrongCA, hasWrongCA := qualificationPEM(t, "NORN_TEST_MYSQL_TLS_WRONG_CA_PEM_B64")
	port, portErr := strconv.Atoi(os.Getenv("NORN_TEST_WORDPRESS_HOST_PORT"))
	wrongCAPort, wrongCAPortErr := strconv.Atoi(os.Getenv("NORN_TEST_WORDPRESS_WRONG_CA_HOST_PORT"))
	wrongHostPort, wrongHostPortErr := strconv.Atoi(os.Getenv("NORN_TEST_WORDPRESS_WRONG_HOST_HOST_PORT"))
	wrongHost := os.Getenv("NORN_TEST_MYSQL_TLS_WRONG_HOST")
	wrongHostExtraHost := os.Getenv("NORN_TEST_MYSQL_TLS_WRONG_HOST_DOCKER_EXTRA_HOST")
	volumeName := os.Getenv("NORN_TEST_WORDPRESS_CONTENT_HOST_VOLUME")
	sentinelPath := os.Getenv("NORN_TEST_WORDPRESS_CONTENT_SENTINEL_PATH")
	if address == "" || host == "" || wrongHost == "" || wrongHost == host || user == "" || password == "" || name == "" || !hasCA || !hasWrongCA || portErr != nil || wrongCAPortErr != nil || wrongHostPortErr != nil || port < 1 || wrongCAPort < 1 || wrongHostPort < 1 || port > 65535 || wrongCAPort > 65535 || wrongHostPort > 65535 || port == wrongCAPort || port == wrongHostPort || wrongCAPort == wrongHostPort || volumeName == "" || sentinelPath == "" {
		t.Skip("set disposable Nomad/MySQL TLS variables, distinct good/wrong-CA/wrong-host WordPress ports, reachable wrong host, and persistent wp-content host-volume sentinel variables")
	}
	if !filepath.IsAbs(sentinelPath) {
		t.Fatal("NORN_TEST_WORDPRESS_CONTENT_SENTINEL_PATH must be absolute")
	}
	sentinel, err := os.ReadFile(sentinelPath)
	if err != nil || len(sentinel) == 0 {
		t.Fatalf("read persistent wp-content sentinel: %v", err)
	}
	if info, err := os.Stat(sentinelPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("persistent wp-content sentinel must be a regular file: %v", err)
	}
	client, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	newJob := func(app string, hostPort int) (*nomadapi.Job, model.ResolvedRegion) {
		t.Helper()
		spec := &model.InfraSpec{SchemaVersion: model.AppSchemaV2, App: app,
			StartupAdapter: model.StartupAdapterWordPressVerifiedTLS,
			Build:          &model.BuildSpec{Image: model.QualifiedWordPressVerifiedTLSImage},
			Processes:      map[string]model.Process{"web": {Port: 80, HostPort: hostPort, Resources: &model.Resources{CPU: 500, Memory: 512}}},
			Volumes:        []model.VolumeSpec{{Name: volumeName, Mount: "/var/www/html/wp-content"}},
			Databases: []model.DatabaseRequirement{{Name: "primary", Purpose: "application", Capabilities: []string{"runtime"}, Runtime: &model.DatabaseRuntime{
				Components: &model.DatabaseRuntimeComponents{Host: "WORDPRESS_DB_HOST", User: "WORDPRESS_DB_USER", Password: "WORDPRESS_DB_PASSWORD", Name: "WORDPRESS_DB_NAME"},
				TLS:        &model.DatabaseRuntimeTLS{CAFileEnv: "MYSQL_SSL_CA"},
			}}},
		}
		if result := model.ValidateSpec(spec); !result.Valid {
			t.Fatalf("product startup-adapter spec invalid: %+v", result.Findings)
		}
		region := spec.ResolvedRegions()[0]
		job := TranslateForRegionAt(spec, spec.Build.Image, nil, region, 7)
		// The disposable fixture has no Consul server and may already cache the
		// immutable image. Neither adjustment changes the translated adapter,
		// templates, volume, or WordPress entrypoint path under qualification.
		job.TaskGroups[0].Services = nil
		job.TaskGroups[0].Tasks[0].Config["force_pull"] = false
		if wrongHostExtraHost != "" {
			job.TaskGroups[0].Tasks[0].Config["extra_hosts"] = []string{wrongHostExtraHost}
		}
		return job, region
	}
	app := fmt.Sprintf("norn-m2-wordpress-adapter-%d", time.Now().UnixNano())
	job, region := newJob(app, port)
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
	if err := client.DeliverDatabaseVariable(region.NomadRegion, jobID, items, 7); err != nil {
		t.Fatal(err)
	}
	wrongJob, wrongRegion := newJob(app+"-wrong-ca", wrongCAPort)
	wrongJobID := *wrongJob.ID
	t.Cleanup(func() {
		_, _, _ = client.api.Jobs().Deregister(wrongJobID, true, nil)
		_, _ = client.api.Variables().Delete(DatabaseVariablePath(wrongJobID), nil)
	})
	wrongItems := map[string]string{
		DatabaseComponentItemKey("primary", "host"): host, DatabaseComponentItemKey("primary", "user"): user,
		DatabaseComponentItemKey("primary", "password"): password, DatabaseComponentItemKey("primary", "name"): name,
		DatabaseTLSItemKey("primary", "ca"): string(wrongCA),
	}
	if err := client.DeliverDatabaseVariable(wrongRegion.NomadRegion, wrongJobID, wrongItems, 7); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.api.Jobs().Register(wrongJob, nil); err != nil {
		t.Fatalf("register product startup-adapter wrong-CA job: %v", err)
	}
	assertWordPressDatabasePage(t, client, wrongJobID, wrongCAPort, false, "product startup-adapter wrong-CA")
	wrongHostJob, wrongHostRegion := newJob(app+"-wrong-host", wrongHostPort)
	wrongHostJobID := *wrongHostJob.ID
	t.Cleanup(func() {
		_, _, _ = client.api.Jobs().Deregister(wrongHostJobID, true, nil)
		_, _ = client.api.Variables().Delete(DatabaseVariablePath(wrongHostJobID), nil)
	})
	wrongHostItems := map[string]string{
		DatabaseComponentItemKey("primary", "host"): wrongHost, DatabaseComponentItemKey("primary", "user"): user,
		DatabaseComponentItemKey("primary", "password"): password, DatabaseComponentItemKey("primary", "name"): name,
		DatabaseTLSItemKey("primary", "ca"): string(ca),
	}
	if err := client.DeliverDatabaseVariable(wrongHostRegion.NomadRegion, wrongHostJobID, wrongHostItems, 7); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.api.Jobs().Register(wrongHostJob, nil); err != nil {
		t.Fatalf("register product startup-adapter wrong-host job: %v", err)
	}
	assertWordPressDatabasePage(t, client, wrongHostJobID, wrongHostPort, false, "product startup-adapter hostname mismatch")
	assertWordPressAllocationTCPReachable(t, client, wrongHostJobID)
	register := func() string {
		t.Helper()
		if _, _, err := client.api.Jobs().Register(job, nil); err != nil {
			t.Fatalf("register product startup-adapter job: %v", err)
		}
		allocationID := assertWordPressDatabasePageWithSentinel(t, client, jobID, port, filepath.Base(sentinelPath), sentinel)
		return allocationID
	}
	firstAllocation := register()
	if _, _, err := client.api.Jobs().Deregister(jobID, true, nil); err != nil {
		t.Fatalf("deregister first startup-adapter allocation: %v", err)
	}
	waitForNoRunningAllocation(t, client.api, jobID)
	// Docker can retain the old host-port binding briefly after Nomad reports
	// the allocation stopped. A fresh port keeps the replacement assertion
	// focused on the same persisted wp-content, rather than daemon teardown.
	replacementPort := freeLocalTCPPort(t)
	replacementJob, _ := newJob(app, replacementPort)
	job = replacementJob
	port = replacementPort
	secondAllocation := register()
	if secondAllocation == firstAllocation {
		t.Fatalf("expected a replacement allocation after deregistration, got %s twice", secondAllocation)
	}
}

func freeLocalTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func assertWordPressAllocationTCPReachable(t *testing.T, client *Client, jobID string) {
	t.Helper()
	allocations, _, err := client.api.Jobs().Allocations(jobID, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, allocation := range allocations {
		if allocation.ClientStatus != "running" {
			continue
		}
		full, _, err := client.api.Allocations().Info(allocation.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		probe := `$target = parse_url('tcp://' . getenv('WORDPRESS_DB_HOST')); $socket = @fsockopen($target['host'], $target['port'], $errno, $error, 5); if (!$socket) { fwrite(STDERR, $error); exit(1); } fclose($socket); echo 'reachable';`
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		exitCode, err := client.api.Allocations().Exec(ctx, full, "web", false, []string{"php", "-r", probe}, strings.NewReader(""), &stdout, &stderr, nil, nil)
		cancel()
		if err != nil || exitCode != 0 || stdout.String() != "reachable" {
			t.Fatalf("wrong-host WordPress allocation TCP reachability: exit=%d stdout=%q stderr=%q err=%v", exitCode, stdout.String(), stderr.String(), err)
		}
		return
	}
	t.Fatalf("wrong-host WordPress job %s has no running allocation for TCP reachability check", jobID)
}

func assertWordPressDatabasePageWithSentinel(t *testing.T, client *Client, jobID string, port int, sentinelName string, wantSentinel []byte) string {
	t.Helper()
	pageURL := fmt.Sprintf("http://127.0.0.1:%d/wp-admin/install.php", port)
	sentinelURL := fmt.Sprintf("http://127.0.0.1:%d/wp-content/%s", port, sentinelName)
	httpClient := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		allocations, _, err := client.api.Jobs().Allocations(jobID, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, allocation := range allocations {
			if allocation.ClientStatus == "failed" || allocation.ClientStatus == "lost" {
				full, _, _ := client.api.Allocations().Info(allocation.ID, nil)
				if full != nil {
					if state := full.TaskStates["web"]; state != nil {
						t.Fatalf("product startup-adapter allocation %s: %s (%s)", allocation.ID, allocation.ClientDescription, taskEventSummary(state.Events))
					}
				}
				t.Fatalf("product startup-adapter allocation %s: %s", allocation.ID, allocation.ClientDescription)
			}
			if allocation.ClientStatus != "running" {
				continue
			}
			response, err := httpClient.Get(pageURL)
			if err != nil {
				continue
			}
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 256<<10))
			_ = response.Body.Close()
			if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "WordPress") || strings.Contains(string(body), "Error establishing a database connection") {
				continue
			}
			sentinelResponse, err := httpClient.Get(sentinelURL)
			if err != nil {
				continue
			}
			gotSentinel, sentinelErr := io.ReadAll(io.LimitReader(sentinelResponse.Body, 64<<10))
			_ = sentinelResponse.Body.Close()
			if sentinelErr == nil && sentinelResponse.StatusCode == http.StatusOK && string(gotSentinel) == string(wantSentinel) {
				return allocation.ID
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("product startup-adapter allocation did not serve the WordPress installation page and persistent wp-content sentinel")
	return ""
}

func waitForNoRunningAllocation(t *testing.T, api *nomadapi.Client, jobID string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		allocations, _, err := api.Jobs().Allocations(jobID, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		running := false
		for _, allocation := range allocations {
			if allocation.ClientStatus == "running" || allocation.ClientStatus == "pending" {
				running = true
				break
			}
		}
		if !running {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s still has a running allocation after deregistration", jobID)
}

func registerWordPressVerifiedTLSDropInJob(t *testing.T, client *Client, host, user, password, name string, ca []byte, port int) string {
	t.Helper()
	dropIn, err := os.ReadFile("wordpress_verified_tls_db.php")
	if err != nil {
		t.Fatal(err)
	}
	data := string(dropIn)
	return registerStockWordPressTLSJobWithMutation(t, client, host, user, password, name, ca, port, func(job *nomadapi.Job) {
		task := job.TaskGroups[0].Tasks[0]
		task.Templates = append(task.Templates, &nomadapi.Template{EmbeddedTmpl: &data, DestPath: strPtr("local/norn-wordpress/db.php"), Perms: strPtr("0444"), ChangeMode: strPtr("restart"), ErrMissingKey: boolPtr(true)})
		startup := "install -D -m 0444 /local/norn-wordpress/db.php /var/www/html/wp-content/db.php\nexec /usr/local/bin/docker-entrypoint.sh apache2-foreground"
		task.Config["command"] = "/bin/sh"
		// The hostname negative uses a Docker host-gateway alias so the TLS
		// handshake reaches the same server through a name outside its SAN. A
		// credential-free TCP preflight makes a later WordPress failure evidence
		// of TLS name verification rather than an unreachable endpoint.
		if strings.HasPrefix(host, "mysql-mismatch:") {
			task.Config["extra_hosts"] = []string{"mysql-mismatch:host-gateway"}
			preflight := `#!/bin/sh
set -eu
host="${WORDPRESS_DB_HOST%:*}"
port="${WORDPRESS_DB_HOST##*:}"
php -r '$socket = @fsockopen($argv[1], (int) $argv[2], $errno, $errstr, 5); if (!$socket) { fwrite(STDERR, "norn-tls-preflight-unreachable\n"); exit(97); } fclose($socket);' "$host" "$port"
echo norn-tls-preflight-reachable >&2
`
			task.Templates = append(task.Templates, &nomadapi.Template{EmbeddedTmpl: &preflight, DestPath: strPtr("local/norn-wordpress/preflight.sh"), Perms: strPtr("0555"), ChangeMode: strPtr("restart"), ErrMissingKey: boolPtr(true)})
			startup = "sh /local/norn-wordpress/preflight.sh\n" + startup
		}
		task.Config["args"] = []string{"-ec", startup}
	})
}

func assertWordPressDatabasePage(t *testing.T, client *Client, jobID string, port int, available bool, control string) {
	t.Helper()
	pageURL := fmt.Sprintf("http://127.0.0.1:%d/wp-admin/install.php", port)
	httpClient := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		allocations, _, err := client.api.Jobs().Allocations(jobID, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, allocation := range allocations {
			if allocation.ClientStatus == "failed" || allocation.ClientStatus == "lost" {
				if state := allocation.TaskStates["web"]; state != nil {
					t.Fatalf("WordPress db.php allocation %s: %s (%s)", allocation.ID, allocation.ClientDescription, taskEventSummary(state.Events))
				}
				t.Fatalf("WordPress db.php allocation %s: %s", allocation.ID, allocation.ClientDescription)
			}
		}
		response, err := httpClient.Get(pageURL)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 256<<10))
			_ = response.Body.Close()
			if readErr == nil {
				text := string(body)
				if available && response.StatusCode == http.StatusOK && strings.Contains(text, "WordPress") && !strings.Contains(text, "Error establishing a database connection") {
					return
				}
				if !available && strings.Contains(text, "Error establishing a database connection") {
					return
				}
			}
		}
		time.Sleep(time.Second)
	}
	if available {
		t.Fatalf("%s WordPress db.php allocation did not reach its installation page", control)
	}
	t.Fatalf("%s WordPress db.php allocation did not reject its database connection over HTTP", control)
}

func taskEventSummary(events []*nomadapi.TaskEvent) string {
	parts := make([]string, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		parts = append(parts, strings.TrimSpace(event.Type+": "+event.DisplayMessage+" "+event.Message))
	}
	return strings.Join(parts, "; ")
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
			if frames == nil {
				// A client may garbage-collect a completed allocation before its
				// logs can be read. Keep the probe bounded by the outer deadline.
				continue
			}
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
