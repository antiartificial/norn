package database

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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

// openMySQLSession supplies local MySQL runtime and verified health adapters.
// Verified TLS material stays private to the session until the caller stages
// it into a revisioned allocation variable. Snapshot, restore and migration
// remain separate protocols.
func openMySQLSession(ctx context.Context, resolved ResolvedBinding, secrets SecretSource) (*Session, error) {
	label := "bindings/" + resolved.Target.BindingID
	if resolved.Purpose != PurposeApplication {
		return nil, &ResolverError{Code: CodePurposeMismatch, Field: "purpose", Resource: label, Reason: "only application targets are opened by the application adapter"}
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
	defer clear(raw)
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
	session := &Session{target: resolved.Target, bindingID: resolved.Target.BindingID, directory: directory, password: secret.Password, endpoint: endpoint}
	config := mysql.NewConfig()
	config.User, config.Passwd, config.DBName = resolved.Target.Role, secret.Password, resolved.Target.Database
	config.Net, config.Addr = "tcp", net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))
	config.Timeout, config.ReadTimeout, config.WriteTimeout = 10*time.Second, 15*time.Second, 15*time.Second
	if resolved.TLS.Mode == TLSDisabled {
		session.runtimeURL = "components"
	} else {
		config.TLS, err = mysqlVerifiedTLS(ctx, resolved.TLS, secrets)
		if err != nil {
			_ = session.Close()
			return nil, &ResolverError{Code: CodeInvalidRequest, Field: "tls", Resource: label, Reason: "MySQL TLS material is invalid or unavailable"}
		}
		session.runtimeTLS, err = mysqlRuntimeTLSMaterial(ctx, resolved.TLS, secrets)
		if err != nil {
			_ = session.Close()
			return nil, &ResolverError{Code: CodeInvalidRequest, Field: "tls", Resource: label, Reason: "MySQL TLS material is invalid or unavailable"}
		}
	}
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
	return session, nil
}

func mysqlRuntimeTLSMaterial(ctx context.Context, binding DatabaseTLS, secrets SecretSource) (map[string][]byte, error) {
	if binding.Mode != TLSVerifyCA && binding.Mode != TLSVerifyFull {
		return nil, errors.New("unsupported MySQL TLS mode")
	}
	refs := map[string]string{"ca": binding.CARef, "client_cert": binding.ClientCertRef, "client_key": binding.ClientKeyRef}
	if refs["ca"] == "" || (refs["client_cert"] == "") != (refs["client_key"] == "") {
		return nil, errors.New("incomplete MySQL TLS material")
	}
	material := make(map[string][]byte, len(refs))
	for name, reference := range refs {
		if reference == "" {
			continue
		}
		value, err := secrets.Resolve(ctx, reference)
		if err != nil || len(value) == 0 || len(value) > maxSecretDocument {
			clear(value)
			for _, copied := range material {
				clear(copied)
			}
			return nil, errors.New("invalid MySQL TLS material")
		}
		material[name] = append([]byte(nil), value...)
		clear(value)
	}
	return material, nil
}

// mysqlVerifiedTLS creates a private, per-connector TLS configuration. The
// driver never falls back to plaintext. verify-ca checks the certificate
// chain without a hostname; verify-full additionally checks ServerName.
func mysqlVerifiedTLS(ctx context.Context, binding DatabaseTLS, secrets SecretSource) (*tls.Config, error) {
	if binding.Mode != TLSVerifyCA && binding.Mode != TLSVerifyFull {
		return nil, errors.New("unsupported MySQL TLS mode")
	}
	caPEM, err := secrets.Resolve(ctx, binding.CARef)
	if err != nil {
		return nil, err
	}
	defer clear(caPEM)
	if len(caPEM) == 0 || len(caPEM) > maxSecretDocument {
		return nil, errors.New("invalid MySQL CA material")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("invalid MySQL CA certificate")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	if binding.Mode == TLSVerifyFull {
		if !serverNamePattern.MatchString(binding.ServerName) {
			return nil, errors.New("invalid MySQL TLS server name")
		}
		config.ServerName = binding.ServerName
	} else {
		// Go's default verifier checks a hostname. For verify-ca, replace it
		// with explicit chain and server-auth verification, without DNSName.
		config.InsecureSkipVerify = true
		config.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("MySQL server certificate is missing")
			}
			intermediates := x509.NewCertPool()
			for _, certificate := range state.PeerCertificates[1:] {
				intermediates.AddCert(certificate)
			}
			_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
			return err
		}
	}
	if binding.ClientCertRef != "" || binding.ClientKeyRef != "" {
		if binding.ClientCertRef == "" || binding.ClientKeyRef == "" {
			return nil, errors.New("MySQL client certificate and key must be paired")
		}
		certPEM, err := secrets.Resolve(ctx, binding.ClientCertRef)
		if err != nil {
			return nil, err
		}
		defer clear(certPEM)
		keyPEM, err := secrets.Resolve(ctx, binding.ClientKeyRef)
		if err != nil {
			return nil, err
		}
		defer clear(keyPEM)
		if len(certPEM) > maxSecretDocument || len(keyPEM) > maxSecretDocument {
			return nil, errors.New("MySQL client certificate material is too large")
		}
		certificate, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, err
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}
