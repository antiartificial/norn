package database

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// TestWordPressImageUsesMySQLRuntimeComponents is an opt-in qualification of
// the ordinary (non-TLS) WordPress runtime contract. It passes exactly the
// four values returned by RuntimeComponents to an unmodified WordPress image,
// then waits for WordPress to reach its HTTP installation page. The companion
// script supplies a disposable MySQL server and pins the image under test.
func TestWordPressImageUsesMySQLRuntimeComponents(t *testing.T) {
	image := os.Getenv("NORN_TEST_WORDPRESS_IMAGE")
	adminDSN := os.Getenv("NORN_TEST_MYSQL_DSN")
	runtimeAddress := os.Getenv("NORN_TEST_WORDPRESS_MYSQL_ENDPOINT")
	if image == "" || adminDSN == "" || runtimeAddress == "" {
		t.Skip("NORN_TEST_WORDPRESS_IMAGE, NORN_TEST_MYSQL_DSN and NORN_TEST_WORDPRESS_MYSQL_ENDPOINT are required")
	}
	adminConfig, err := mysql.ParseDSN(adminDSN)
	if err != nil || adminConfig.Net != "tcp" {
		t.Fatal("NORN_TEST_MYSQL_DSN must be a TCP MySQL DSN")
	}
	host, portText, err := splitDatabaseEndpoint(runtimeAddress)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal("WordPress MySQL endpoint has an invalid port")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin := sql.OpenDB(mustMySQLConnector(t, adminConfig))
	defer admin.Close()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatal("disposable MySQL server is unavailable")
	}
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	databaseName, role := "wp_"+suffix, "wp_"+suffix
	password := `WordPress runtime "quote" \\ space ` + suffix
	defer func() {
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+databaseName+"`")
		_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+role+"'@'%'")
	}()
	for _, statement := range []string{
		"CREATE DATABASE `" + databaseName + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		"CREATE USER '" + role + "'@'%' IDENTIFIED BY " + mysqlQuote(password),
		"GRANT ALL PRIVILEGES ON `" + databaseName + "`.* TO '" + role + "'@'%'",
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatal("prepare disposable WordPress target failed")
		}
	}

	resolved := ResolvedBinding{Target: TargetIdentity{ServiceID: "mysql-local", ServiceGeneration: 1, BindingID: "wordpress", BindingGeneration: 1,
		Engine: EngineMySQL, Database: databaseName, Role: role}, Purpose: PurposeApplication, CredentialRef: "secret:wordpress",
		Endpoint: DatabaseEndpoint{Host: host, Port: port}, TLS: DatabaseTLS{Mode: TLSDisabled}}
	session, err := OpenSession(ctx, resolved, literalSecrets{"secret:wordpress": fmt.Sprintf(`{"password":%q}`, password)})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	components, err := session.RuntimeComponents()
	if err != nil {
		t.Fatal(err)
	}

	container := "norn-wordpress-runtime-" + suffix
	args := []string{"run", "--detach", "--name", container, "--add-host", "host.docker.internal:host-gateway", "--publish", "127.0.0.1::80"}
	for _, pair := range []struct{ name, value string }{
		{"WORDPRESS_DB_HOST", components["host"]}, {"WORDPRESS_DB_USER", components["user"]},
		{"WORDPRESS_DB_PASSWORD", components["password"]}, {"WORDPRESS_DB_NAME", components["name"]},
	} {
		args = append(args, "--env", pair.name+"="+pair.value)
	}
	args = append(args, image)
	if output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("start WordPress image: %v: %s", err, strings.TrimSpace(string(output)))
	}
	defer exec.Command("docker", "rm", "--force", container).Run()

	portOutput, err := exec.CommandContext(ctx, "docker", "port", container, "80/tcp").Output()
	if err != nil {
		t.Fatal("inspect WordPress HTTP port")
	}
	_, httpPort, err := splitDatabaseEndpoint(strings.TrimSpace(string(portOutput)))
	if err != nil {
		t.Fatal("WordPress HTTP port was malformed")
	}
	url := "http://127.0.0.1:" + httpPort + "/wp-admin/install.php"
	client := &http.Client{Timeout: 5 * time.Second}
	var last string
	for ctx.Err() == nil {
		response, requestErr := client.Get(url)
		if requestErr == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			last = fmt.Sprintf("HTTP %d", response.StatusCode)
			if readErr == nil && response.StatusCode == http.StatusOK && strings.Contains(string(body), "WordPress &rsaquo; Installation") {
				return
			}
		} else {
			last = requestErr.Error()
		}
		time.Sleep(500 * time.Millisecond)
	}
	logs, _ := exec.Command("docker", "logs", "--tail", "40", container).CombinedOutput()
	t.Fatalf("WordPress did not reach its installation page (%s): %s", last, strings.TrimSpace(string(logs)))
}

func splitDatabaseEndpoint(value string) (string, string, error) {
	index := strings.LastIndex(value, ":")
	if index < 1 || index == len(value)-1 {
		return "", "", fmt.Errorf("endpoint must be host:port")
	}
	return strings.Trim(value[:index], "[]"), value[index+1:], nil
}

func mysqlQuote(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(value) + "'"
}
