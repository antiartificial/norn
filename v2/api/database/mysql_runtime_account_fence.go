package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// mysqlRuntimeAccountFence is intentionally private. It is a recovery
// primitive, not a catalog capability or API route. The fence credential must
// be a separately provisioned MySQL account with CREATE USER, PROCESS,
// CONNECTION_ADMIN, and SELECT on mysql.user authority; it is never the
// runtime or restore credential. The mysql.user read is a deliberate private
// prototype admission proof; a narrower definer/provisioner path is future
// hardening work.
//
// DedicatedRuntimeUsername is an explicit admission claim: MySQL's process
// list reports USER but not the matched account host, so session termination
// is safe only when this runtime username is dedicated to this workload.
type mysqlRuntimeAccountFence struct {
	FenceUser                string
	FenceAccountHost         string
	FenceCredentialRef       string
	RuntimeAccountHost       string
	DedicatedRuntimeUsername string
}

// fenceMySQLRuntimeAccount locks one exact MySQL account, terminates all
// existing sessions for its dedicated username, and proves the account remains
// locked with no such sessions. It fails closed on every incomplete proof.
func fenceMySQLRuntimeAccount(ctx context.Context, resolved ResolvedBinding, fence mysqlRuntimeAccountFence, secrets SecretSource) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	label := "bindings/" + resolved.Target.BindingID
	if resolved.Target.Engine != EngineMySQL || resolved.Purpose != PurposeApplication || !validMySQLFence(resolved, fence) {
		return &ResolverError{Code: CodeInvalidRequest, Field: "runtimeAccountFence", Resource: label, Reason: "MySQL runtime account fence admission is invalid"}
	}
	if secrets == nil {
		return &ResolverError{Code: CodeInvalidRequest, Field: "runtimeAccountFence", Resource: label, Reason: "MySQL runtime account fence secret source is unavailable"}
	}
	raw, err := secrets.Resolve(ctx, fence.FenceCredentialRef)
	if err != nil {
		return &ResolverError{Code: CodeInvalidRequest, Field: "runtimeAccountFence", Resource: label, Reason: "MySQL runtime account fence credential could not be resolved"}
	}
	defer clear(raw)
	secret, err := decodeConnectionSecret(raw)
	if err != nil || containsLineBreak(secret.Password) {
		return &ResolverError{Code: CodeInvalidRequest, Field: "runtimeAccountFence", Resource: label, Reason: "MySQL runtime account fence credential is invalid"}
	}

	db, err := openMySQLFenceDB(ctx, resolved, fence.FenceUser, secret.Password, secrets)
	if err != nil {
		return &ResolverError{Code: CodeInvalidRequest, Field: "runtimeAccountFence", Resource: label, Reason: "MySQL runtime account fence connection material is invalid"}
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("MySQL runtime account fence connection failed")
	}
	if err := verifyMySQLFenceIdentity(ctx, db, fence); err != nil {
		return err
	}
	if err := verifyDedicatedMySQLRuntimeUsername(ctx, db, resolved.Target.Role, fence.RuntimeAccountHost); err != nil {
		return err
	}
	account := mysqlAccountLiteral(resolved.Target.Role, fence.RuntimeAccountHost)
	if _, err := db.ExecContext(ctx, "ALTER USER "+account+" ACCOUNT LOCK"); err != nil {
		return fmt.Errorf("MySQL runtime account lock failed")
	}
	if err := terminateMySQLRuntimeSessions(ctx, db, resolved.Target.Role); err != nil {
		return err
	}
	if err := verifyMySQLRuntimeAccountFence(ctx, db, resolved.Target.Role, fence.RuntimeAccountHost); err != nil {
		return err
	}
	return nil
}

// unfenceMySQLRuntimeAccount is deliberately separate from fencing. Callers
// must make an explicit recovery decision before restoring authentication.
func unfenceMySQLRuntimeAccount(ctx context.Context, resolved ResolvedBinding, fence mysqlRuntimeAccountFence, secrets SecretSource) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if resolved.Target.Engine != EngineMySQL || resolved.Purpose != PurposeApplication || !validMySQLFence(resolved, fence) || secrets == nil {
		return errors.New("MySQL runtime account unfence admission is invalid")
	}
	raw, err := secrets.Resolve(ctx, fence.FenceCredentialRef)
	if err != nil {
		return errors.New("MySQL runtime account unfence credential could not be resolved")
	}
	defer clear(raw)
	secret, err := decodeConnectionSecret(raw)
	if err != nil || containsLineBreak(secret.Password) {
		return errors.New("MySQL runtime account unfence credential is invalid")
	}
	db, err := openMySQLFenceDB(ctx, resolved, fence.FenceUser, secret.Password, secrets)
	if err != nil {
		return errors.New("MySQL runtime account unfence connection material is invalid")
	}
	defer db.Close()
	if err := verifyMySQLFenceIdentity(ctx, db, fence); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "ALTER USER "+mysqlAccountLiteral(resolved.Target.Role, fence.RuntimeAccountHost)+" ACCOUNT UNLOCK"); err != nil {
		return errors.New("MySQL runtime account unlock failed")
	}
	return nil
}

// openMySQLFenceDB binds the control connection to the same endpoint and TLS
// verification policy as the runtime account. A fence never downgrades a
// verified target to plaintext.
func openMySQLFenceDB(ctx context.Context, resolved ResolvedBinding, user, password string, secrets SecretSource) (*sql.DB, error) {
	config := mysql.NewConfig()
	config.User, config.Passwd, config.Net = user, password, "tcp"
	config.Addr = net.JoinHostPort(resolved.Endpoint.Host, strconv.Itoa(resolved.Endpoint.Port))
	config.Timeout, config.ReadTimeout, config.WriteTimeout = 10*time.Second, 15*time.Second, 15*time.Second
	if resolved.TLS.Mode != TLSDisabled {
		tlsConfig, err := mysqlVerifiedTLS(ctx, resolved.TLS, secrets)
		if err != nil {
			return nil, err
		}
		config.TLS = tlsConfig
	}
	connector, err := mysql.NewConnector(config)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}

func validMySQLFence(resolved ResolvedBinding, fence mysqlRuntimeAccountFence) bool {
	return validEndpointHost(resolved.Endpoint.Host) && !strings.HasPrefix(resolved.Endpoint.Host, "/") && resolved.Endpoint.Port >= 1 && resolved.Endpoint.Port <= 65535 &&
		mysqlUserPattern.MatchString(resolved.Target.Role) && mysqlUserPattern.MatchString(fence.FenceUser) &&
		fence.FenceUser != resolved.Target.Role && fence.FenceCredentialRef != "" && fence.FenceCredentialRef != resolved.CredentialRef &&
		fence.DedicatedRuntimeUsername == resolved.Target.Role && validMySQLAccountHost(fence.RuntimeAccountHost) && validMySQLAccountHost(fence.FenceAccountHost)
}

func verifyMySQLFenceIdentity(ctx context.Context, db *sql.DB, fence mysqlRuntimeAccountFence) error {
	var authenticated string
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&authenticated); err != nil || authenticated != fence.FenceUser+"@"+fence.FenceAccountHost {
		return errors.New("MySQL runtime fence authenticated as an unexpected account")
	}
	return nil
}

func validMySQLAccountHost(host string) bool {
	return len(host) > 0 && len(host) <= 255 && !strings.ContainsAny(host, "\x00\r\n")
}

func mysqlAccountLiteral(user, host string) string {
	return "'" + strings.ReplaceAll(user, "'", "''") + "'@'" + strings.ReplaceAll(host, "'", "''") + "'"
}

func terminateMySQLRuntimeSessions(ctx context.Context, db *sql.DB, username string) error {
	zeroObservations := 0
	for {
		ids, err := mysqlRuntimeSessionIDs(ctx, db, username)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			zeroObservations++
			if zeroObservations == 2 {
				return nil
			}
		} else {
			zeroObservations = 0
			for _, id := range ids {
				if _, err := db.ExecContext(ctx, "KILL CONNECTION "+strconv.FormatUint(id, 10)); err != nil {
					return errors.New("MySQL runtime session termination failed")
				}
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("MySQL runtime session fence verification timed out")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func mysqlRuntimeSessionIDs(ctx context.Context, db *sql.DB, username string) ([]uint64, error) {
	rows, err := db.QueryContext(ctx, "SELECT ID FROM INFORMATION_SCHEMA.PROCESSLIST WHERE USER = ?", username)
	if err != nil {
		return nil, errors.New("MySQL runtime session inspection failed")
	}
	defer rows.Close()
	var ids []uint64
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return nil, errors.New("MySQL runtime session inspection failed")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("MySQL runtime session inspection failed")
	}
	return ids, nil
}

// verifyDedicatedMySQLRuntimeUsername makes the admission claim observable:
// the runtime username must have exactly one account definition, at the exact
// host we are about to lock. Without this proof PROCESSLIST.USER is too broad
// to safely target sessions.
func verifyDedicatedMySQLRuntimeUsername(ctx context.Context, db *sql.DB, username, expectedHost string) error {
	rows, err := db.QueryContext(ctx, "SELECT Host FROM mysql.user WHERE User = ?", username)
	if err != nil {
		return errors.New("MySQL runtime account uniqueness verification failed")
	}
	defer rows.Close()
	var hosts []string
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			return errors.New("MySQL runtime account uniqueness verification failed")
		}
		hosts = append(hosts, host)
	}
	if err := rows.Err(); err != nil || len(hosts) != 1 || hosts[0] != expectedHost {
		return errors.New("MySQL runtime account uniqueness verification failed")
	}
	return nil
}

func verifyMySQLRuntimeAccountFence(ctx context.Context, db *sql.DB, username, host string) error {
	var locked string
	err := db.QueryRowContext(ctx, "SELECT account_locked FROM mysql.user WHERE User = ? AND Host = ?", username, host).Scan(&locked)
	if err != nil || locked != "Y" {
		return errors.New("MySQL runtime account lock verification failed")
	}
	return terminateMySQLRuntimeSessions(ctx, db, username)
}
