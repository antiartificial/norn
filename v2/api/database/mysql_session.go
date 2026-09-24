package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// openMySQLSession supplies the local MySQL runtime and health adapter. Its
// snapshot, restore, migration and TLS paths remain unavailable until their
// separate engine protocols are implemented and qualified.
func openMySQLSession(ctx context.Context, resolved ResolvedBinding, secrets SecretSource) (*Session, error) {
	label := "bindings/" + resolved.Target.BindingID
	if resolved.Purpose != PurposeApplication {
		return nil, &ResolverError{Code: CodePurposeMismatch, Field: "purpose", Resource: label, Reason: "only application targets are opened by the application adapter"}
	}
	if resolved.TLS.Mode != TLSDisabled {
		return nil, &ResolverError{Code: CodeUnsupportedCapability, Field: "tls", Resource: label, Reason: "MySQL TLS connection material is not implemented"}
	}
	if secrets == nil {
		return nil, &ResolverError{Code: CodeInvalidRequest, Field: "credentialRef", Resource: label, Reason: "no secret source is configured"}
	}
	endpoint := resolved.Endpoint
	if !validEndpointHost(endpoint.Host) || strings.HasPrefix(endpoint.Host, "/") || endpoint.Port < 1 || endpoint.Port > 65535 {
		return nil, &ResolverError{Code: CodeInvalidRequest, Field: "endpoint", Resource: label, Reason: "MySQL target requires a valid TCP endpoint"}
	}
	raw, err := secrets.Resolve(ctx, resolved.CredentialRef)
	if err != nil {
		return nil, &ResolverError{Code: CodeInvalidRequest, Field: "credentialRef", Resource: label, Reason: "credential reference could not be resolved"}
	}
	secret, err := decodeConnectionSecret(raw)
	if err != nil || containsLineBreak(secret.Password) {
		return nil, &ResolverError{Code: CodeInvalidRequest, Field: "credentialRef", Resource: label, Reason: "connection secret must be one strict JSON object containing only an optional password"}
	}
	directory, err := os.MkdirTemp("", "norn-mysql-target-*")
	if err != nil {
		return nil, fmt.Errorf("prepare private database material: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	session := &Session{target: resolved.Target, bindingID: resolved.Target.BindingID, directory: directory, password: secret.Password, endpoint: endpoint, runtimeURL: "components"}
	config := mysql.NewConfig()
	config.User, config.Passwd, config.DBName = resolved.Target.Role, secret.Password, resolved.Target.Database
	config.Net, config.Addr = "tcp", net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))
	config.Timeout, config.ReadTimeout, config.WriteTimeout = 10*time.Second, 15*time.Second, 15*time.Second
	connector, err := mysql.NewConnector(config)
	if err != nil {
		_ = session.Close()
		return nil, &ResolverError{Code: CodeInvalidRequest, Field: "credentialRef", Resource: label, Reason: "MySQL connection material is invalid"}
	}
	session.mysqlProbe = func(ctx context.Context) (ProbeResult, error) {
		db := sql.OpenDB(connector)
		defer db.Close()
		var result ProbeResult
		if err := db.QueryRowContext(ctx, `SELECT DATABASE(), SUBSTRING_INDEX(CURRENT_USER(), '@', 1), VERSION()`).Scan(&result.Database, &result.Role, &result.ServerVersion); err != nil {
			var mysqlErr *mysql.MySQLError
			if errors.As(err, &mysqlErr) {
				return ProbeResult{}, fmt.Errorf("database target %s probe failed (MySQL error %d)", session.bindingID, mysqlErr.Number)
			}
			return ProbeResult{}, fmt.Errorf("database target %s probe failed", session.bindingID)
		}
		if result.Database != session.target.Database || result.Role != session.target.Role {
			return result, &ResolverError{Code: CodeStaleTarget, Field: "probe", Resource: label, Reason: "connected database or role differs from the declared target"}
		}
		return result, nil
	}
	// Avoid keeping raw JSON secret bytes in the session after parsing.
	for i := range raw {
		raw[i] = 0
	}
	return session, nil
}
