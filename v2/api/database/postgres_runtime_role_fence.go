package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// FencePostgresRuntimeRoleForCutover is a private external-effect primitive.
// The caller must first commit a durable cutover intent, hold its operation
// claim, inventory all writers, and re-resolve the exact catalog target.
// This function fences only sessions authenticated as the named runtime role;
// it does not authorize activation or cover integrations using other roles.
func FencePostgresRuntimeRoleForCutover(ctx context.Context, resolved ResolvedBinding, expected TargetIdentity, secrets SecretSource) error {
	if err := validatePostgresRuntimeFenceRequest(resolved, expected, secrets); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	connection, closeSession, err := openPostgresFenceConnection(ctx, resolved, secrets)
	if err != nil {
		return err
	}
	defer closeSession()
	if err := verifyPostgresFencePrincipal(ctx, connection, resolved); err != nil {
		return err
	}
	role := pgx.Identifier{resolved.Target.Role}.Sanitize()
	if _, err := connection.Exec(ctx, "ALTER ROLE "+role+" NOLOGIN"); err != nil {
		return fmt.Errorf("PostgreSQL runtime login fence failed")
	}
	for attempt := 0; attempt < 3; attempt++ {
		rows, err := connection.Query(ctx, `SELECT pid FROM pg_stat_activity WHERE usename = $1 AND pid <> pg_backend_pid()`, resolved.Target.Role)
		if err != nil {
			return fmt.Errorf("PostgreSQL runtime session inventory failed")
		}
		var pids []int32
		for rows.Next() {
			var pid int32
			if err := rows.Scan(&pid); err != nil {
				rows.Close()
				return fmt.Errorf("PostgreSQL runtime session inventory failed")
			}
			pids = append(pids, pid)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("PostgreSQL runtime session inventory failed")
		}
		for _, pid := range pids {
			var terminated bool
			err := connection.QueryRow(ctx, `
				SELECT pg_terminate_backend(pid) FROM pg_stat_activity
				WHERE pid = $1 AND usename = $2`, pid, resolved.Target.Role).Scan(&terminated)
			if errors.Is(err, pgx.ErrNoRows) {
				// The session left between inventory and termination. The
				// final readback still has to prove that no runtime sessions remain.
				continue
			}
			if err != nil || !terminated {
				return fmt.Errorf("PostgreSQL runtime session termination failed")
			}
		}
		if err := inspectPostgresRuntimeFence(ctx, connection, resolved.Target.Role); err == nil {
			return nil
		} else if attempt == 2 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("PostgreSQL runtime role fence remained unverified")
}

// InspectPostgresRuntimeRoleFenceForCutover checks the role and its sessions
// through the same separately provisioned maintenance credential.
func InspectPostgresRuntimeRoleFenceForCutover(ctx context.Context, resolved ResolvedBinding, expected TargetIdentity, secrets SecretSource) error {
	if err := validatePostgresRuntimeFenceRequest(resolved, expected, secrets); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	connection, closeSession, err := openPostgresFenceConnection(ctx, resolved, secrets)
	if err != nil {
		return err
	}
	defer closeSession()
	if err := verifyPostgresFencePrincipal(ctx, connection, resolved); err != nil {
		return err
	}
	return inspectPostgresRuntimeFence(ctx, connection, resolved.Target.Role)
}

// PostgresRuntimeFencePreflight is read-only evidence about the exact
// catalog-bound source role. It does not prove that an external writer uses no
// other role or that the provider will allow a later ALTER/termination effect.
type PostgresRuntimeFencePreflight struct {
	Target                 TargetIdentity
	FenceGeneration        uint64
	ServerVersion          int
	CanLogin               bool
	Sessions               int
	DatabaseClientSessions []PostgresClientRoleSessions
}

// PostgresClientRoleSessions counts current client backends for one database.
// It is not a privilege inventory and cannot find dormant credentials.
type PostgresClientRoleSessions struct {
	Role     string `json:"role"`
	Sessions int    `json:"sessions"`
}

// PreflightPostgresRuntimeRoleFenceForCutover checks the selected maintenance
// principal and observes the source runtime role without changing either one.
// A coordinator must retain this alongside its writer inventory, then repeat
// authority checks under a durable claim immediately before the fence effect.
func PreflightPostgresRuntimeRoleFenceForCutover(ctx context.Context, resolved ResolvedBinding, expected TargetIdentity, secrets SecretSource) (*PostgresRuntimeFencePreflight, error) {
	if err := validatePostgresRuntimeFenceRequest(resolved, expected, secrets); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	connection, closeSession, err := openPostgresFenceConnection(ctx, resolved, secrets)
	if err != nil {
		return nil, err
	}
	defer closeSession()
	if err := verifyPostgresFencePrincipal(ctx, connection, resolved); err != nil {
		return nil, err
	}
	result := &PostgresRuntimeFencePreflight{Target: resolved.Target, FenceGeneration: resolved.PostgresFence.Generation}
	err = connection.QueryRow(ctx, `
		SELECT current_setting('server_version_num')::int, runtime.rolcanlogin,
			(SELECT count(*) FROM pg_stat_activity WHERE usename = $1)
		FROM pg_roles runtime WHERE runtime.rolname = $1`, resolved.Target.Role).Scan(
		&result.ServerVersion, &result.CanLogin, &result.Sessions)
	if err != nil || result.ServerVersion < 160000 || result.Sessions < 0 {
		return nil, fmt.Errorf("PostgreSQL runtime fence preflight readback failed")
	}
	rows, err := connection.Query(ctx, `
		SELECT COALESCE(usename, '<unknown>'), count(*)::int
		FROM pg_stat_activity
		WHERE datname = current_database() AND backend_type = 'client backend'
		GROUP BY COALESCE(usename, '<unknown>')
		ORDER BY COALESCE(usename, '<unknown>')`)
	if err != nil {
		return nil, fmt.Errorf("PostgreSQL database client session inventory failed")
	}
	defer rows.Close()
	for rows.Next() {
		var item PostgresClientRoleSessions
		if rows.Scan(&item.Role, &item.Sessions) != nil || item.Role == "" || item.Sessions < 1 {
			return nil, fmt.Errorf("PostgreSQL database client session inventory failed")
		}
		result.DatabaseClientSessions = append(result.DatabaseClientSessions, item)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("PostgreSQL database client session inventory failed")
	}
	return result, nil
}

func validatePostgresRuntimeFenceRequest(resolved ResolvedBinding, expected TargetIdentity, secrets SecretSource) error {
	fence := resolved.PostgresFence
	if secrets == nil || resolved.Target != expected || resolved.Purpose != PurposeApplication ||
		resolved.Target.Engine != EnginePostgreSQL || fence == nil || fence.Generation == 0 ||
		!postgresRolePattern.MatchString(resolved.Target.Role) || !postgresRolePattern.MatchString(fence.Role) ||
		fence.Role == resolved.Target.Role || !referencePattern.MatchString(fence.CredentialRef) ||
		fence.CredentialRef == resolved.CredentialRef {
		return fmt.Errorf("PostgreSQL runtime fence admission is invalid")
	}
	return nil
}

func openPostgresFenceConnection(ctx context.Context, resolved ResolvedBinding, secrets SecretSource) (*pgx.Conn, func(), error) {
	maintenance := resolved
	maintenance.Target.Role = resolved.PostgresFence.Role
	maintenance.CredentialRef = resolved.PostgresFence.CredentialRef
	maintenance.PostgresFence = nil
	session, err := OpenSession(ctx, maintenance, secrets)
	if err != nil {
		return nil, nil, fmt.Errorf("PostgreSQL fence connection material is unavailable")
	}
	connection, err := pgx.ConnectConfig(ctx, session.config.Copy())
	if err != nil {
		session.Close()
		return nil, nil, fmt.Errorf("PostgreSQL fence connection failed")
	}
	return connection, func() {
		connection.Close(context.Background())
		session.Close()
	}, nil
}

func verifyPostgresFencePrincipal(ctx context.Context, connection *pgx.Conn, resolved ResolvedBinding) error {
	var current string
	var superuser, createRole, signalBackend, readStats, roleAdmin, roleInherit, roleSet, runtimeSuperuser bool
	err := connection.QueryRow(ctx, `
		SELECT current_user, fence.rolsuper, fence.rolcreaterole,
			pg_has_role(current_user, 'pg_signal_backend', 'USAGE'),
			pg_has_role(current_user, 'pg_read_all_stats', 'USAGE'),
			membership.admin_option, membership.inherit_option, membership.set_option,
			runtime.rolsuper
		FROM pg_roles fence
		JOIN pg_roles runtime ON runtime.rolname = $1
		JOIN pg_auth_members membership ON membership.roleid = runtime.oid AND membership.member = fence.oid
		WHERE fence.rolname = current_user`, resolved.Target.Role).Scan(
		&current, &superuser, &createRole, &signalBackend, &readStats,
		&roleAdmin, &roleInherit, &roleSet, &runtimeSuperuser)
	if err != nil || current != resolved.PostgresFence.Role || superuser || !createRole || !signalBackend || !readStats ||
		!roleAdmin || roleInherit || roleSet || runtimeSuperuser {
		return fmt.Errorf("PostgreSQL fence principal authority does not match the catalog-bound role")
	}
	return nil
}

func inspectPostgresRuntimeFence(ctx context.Context, connection *pgx.Conn, role string) error {
	var canLogin bool
	var sessions int
	if err := connection.QueryRow(ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname = $1`, role).Scan(&canLogin); err != nil || canLogin {
		return fmt.Errorf("PostgreSQL runtime login fence is not active")
	}
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE usename = $1`, role).Scan(&sessions); err != nil || sessions != 0 {
		return fmt.Errorf("PostgreSQL runtime role still has sessions")
	}
	return nil
}
