package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// This opt-in test uses a disposable real MySQL server. It proves that
// catalog identity, private credential material and the WordPress-style
// runtime values all name the same database and account.
func TestMySQLRuntimeComponentsReachDeclaredTarget(t *testing.T) {
	adminDSN := os.Getenv("NORN_TEST_MYSQL_DSN")
	if adminDSN == "" {
		t.Skip("NORN_TEST_MYSQL_DSN is not set")
	}
	adminConfig, err := mysql.ParseDSN(adminDSN)
	if err != nil || adminConfig.Net != "tcp" {
		t.Fatal("NORN_TEST_MYSQL_DSN must be a TCP MySQL DSN")
	}
	admin := sql.OpenDB(mustMySQLConnector(t, adminConfig))
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatal("disposable MySQL server is unavailable")
	}
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	databaseName, role := "norn_"+suffix, "norn_"+suffix
	const password = "MySQL-runtime-canary!7?"
	defer func() {
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+databaseName+"`")
		_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+role+"'@'%'")
	}()
	for _, statement := range []string{
		"CREATE DATABASE `" + databaseName + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		"CREATE USER '" + role + "'@'%' IDENTIFIED BY '" + password + "'",
		"GRANT ALL PRIVILEGES ON `" + databaseName + "`.* TO '" + role + "'@'%'",
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatal("prepare disposable MySQL target failed")
		}
	}
	host, portText, err := net.SplitHostPort(adminConfig.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal("invalid disposable MySQL port")
	}
	resolved := ResolvedBinding{Target: TargetIdentity{ServiceID: "mysql-local", ServiceGeneration: 1, BindingID: "wordpress", BindingGeneration: 1,
		Engine: EngineMySQL, Database: databaseName, Role: role}, Purpose: PurposeApplication, CredentialRef: "secret:mysql",
		Endpoint: DatabaseEndpoint{Host: host, Port: port}, TLS: DatabaseTLS{Mode: TLSDisabled}}
	session, err := OpenSession(ctx, resolved, literalSecrets{"secret:mysql": `{"password":"` + password + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	probe, err := session.Probe(ctx)
	if err != nil || probe.Database != databaseName || probe.Role != role || probe.ServerVersion == "" {
		t.Fatalf("MySQL identity probe = %+v, %v", probe, err)
	}
	if caPath := os.Getenv("NORN_TEST_MYSQL_CA_PEM"); caPath != "" {
		caPEM, err := os.ReadFile(caPath)
		if err != nil {
			t.Fatal(err)
		}
		tlsResolved := resolved
		tlsResolved.TLS = DatabaseTLS{Mode: TLSVerifyCA, CARef: "secret:mysql-ca"}
		tlsSession, err := OpenSession(ctx, tlsResolved, literalSecrets{"secret:mysql": `{"password":"` + password + `"}`, "secret:mysql-ca": string(caPEM)})
		if err != nil {
			t.Fatal(err)
		}
		defer tlsSession.Close()
		tlsProbe, err := tlsSession.Probe(ctx)
		if err != nil || tlsProbe.Database != databaseName || tlsProbe.Role != role {
			t.Fatalf("verified MySQL TLS probe = %+v, %v", tlsProbe, err)
		}
		if components, err := tlsSession.RuntimeComponents(); err != nil || components["host"] != adminConfig.Addr {
			t.Fatalf("TLS MySQL session runtime components = %v, %v", components, err)
		}
		if material, err := tlsSession.RuntimeTLSMaterial(); err != nil || string(material["ca"]) != string(caPEM) {
			t.Fatal("TLS MySQL session did not expose matching CA material")
		}
		if serverName := os.Getenv("NORN_TEST_MYSQL_SERVER_NAME"); serverName != "" {
			full := tlsResolved
			full.TLS = DatabaseTLS{Mode: TLSVerifyFull, CARef: "secret:mysql-ca", ServerName: serverName}
			fullSession, err := OpenSession(ctx, full, literalSecrets{"secret:mysql": `{"password":"` + password + `"}`, "secret:mysql-ca": string(caPEM)})
			if err != nil {
				t.Fatal(err)
			}
			defer fullSession.Close()
			if fullProbe, err := fullSession.Probe(ctx); err != nil || fullProbe.Database != databaseName || fullProbe.Role != role {
				t.Fatalf("verify-full MySQL identity probe = %+v, %v", fullProbe, err)
			}
		}
		unrelatedServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		unrelatedCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: unrelatedServer.Certificate().Raw})
		unrelatedServer.Close()
		wrongCASession, err := OpenSession(ctx, tlsResolved, literalSecrets{"secret:mysql": `{"password":"` + password + `"}`, "secret:mysql-ca": string(unrelatedCA)})
		if err != nil {
			t.Fatal(err)
		}
		defer wrongCASession.Close()
		if _, err := wrongCASession.Probe(ctx); err == nil {
			t.Fatal("MySQL adapter accepted an unrelated CA")
		}
		wrongHost := tlsResolved
		wrongHost.TLS = DatabaseTLS{Mode: TLSVerifyFull, CARef: "secret:mysql-ca", ServerName: "wrong.example"}
		wrongHostSession, err := OpenSession(ctx, wrongHost, literalSecrets{"secret:mysql": `{"password":"` + password + `"}`, "secret:mysql-ca": string(caPEM)})
		if err != nil {
			t.Fatal(err)
		}
		defer wrongHostSession.Close()
		if _, err := wrongHostSession.Probe(ctx); err == nil {
			t.Fatal("MySQL adapter accepted a wrong server name")
		}
	}
	values, err := session.RuntimeComponents()
	if err != nil || values["host"] != adminConfig.Addr || values["user"] != role || values["password"] != password || values["name"] != databaseName {
		t.Fatal("MySQL runtime components do not match the probed target")
	}
	if _, err := session.RuntimeConnectionURL(); err == nil {
		t.Fatal("MySQL was delivered as a PostgreSQL-style URL")
	}
	ordinary := mysql.NewConfig()
	ordinary.Net, ordinary.Addr, ordinary.User, ordinary.Passwd, ordinary.DBName = "tcp", values["host"], values["user"], values["password"], values["name"]
	client := sql.OpenDB(mustMySQLConnector(t, ordinary))
	defer client.Close()
	if _, err := client.ExecContext(ctx, "CREATE TABLE marker (value VARCHAR(64))"); err != nil {
		t.Fatal("ordinary client could not create data")
	}
	if _, err := client.ExecContext(ctx, "INSERT INTO marker VALUES ('wordpress-runtime-ok')"); err != nil {
		t.Fatal("ordinary client could not write data")
	}
	var got string
	if err := client.QueryRowContext(ctx, "SELECT value FROM marker").Scan(&got); err != nil || got != "wordpress-runtime-ok" {
		t.Fatalf("ordinary client read = %q, %v", got, err)
	}
	wrong := *ordinary
	wrong.Passwd = "wrong"
	wrongClient := sql.OpenDB(mustMySQLConnector(t, &wrong))
	defer wrongClient.Close()
	if err := wrongClient.PingContext(ctx); err == nil {
		t.Fatal("wrong MySQL credential was accepted")
	}
	if redacted := session.Redact([]byte(fmt.Sprintf("%s %s", password, values["password"]))); strings.Contains(redacted, password) {
		t.Fatal("MySQL credential appeared in redacted output")
	}
}

func mustMySQLConnector(t *testing.T, config *mysql.Config) driver.Connector {
	t.Helper()
	connector, err := mysql.NewConnector(config)
	if err != nil {
		t.Fatal("invalid MySQL test connection")
	}
	return connector
}
