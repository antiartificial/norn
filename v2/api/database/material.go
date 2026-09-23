package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"norn/v2/api/capture"
)

const (
	// ServiceName is the only connection value that appears in command
	// arguments; everything else lives in the private service file.
	ServiceName           = "norn_target"
	maxSecretDocument     = 64 << 10
	secretReferenceScheme = "secret:"
)

// SecretSource resolves catalog secret references to private bytes. It is
// the only path by which credential or TLS material enters Norn.
type SecretSource interface {
	Resolve(ctx context.Context, reference string) ([]byte, error)
}

// DirectorySecretSource resolves "secret:<relative path>" references inside
// one private directory. Paths cannot escape the directory (including via
// symlinks), and files must be regular, owner-only and owned by this user.
type DirectorySecretSource struct {
	root *os.Root
}

func NewDirectorySecretSource(path string) (*DirectorySecretSource, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("database secret directory must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("database secret directory must be an owner-only directory")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("database secret directory must be owned by this user")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open database secret directory: %w", err)
	}
	return &DirectorySecretSource{root: root}, nil
}

func (s *DirectorySecretSource) Close() error { return s.root.Close() }

func (s *DirectorySecretSource) Resolve(_ context.Context, reference string) ([]byte, error) {
	unresolved := fmt.Errorf("database secret reference could not be resolved")
	if s == nil || s.root == nil || !referencePattern.MatchString(reference) || !strings.HasPrefix(reference, secretReferenceScheme) {
		return nil, unresolved
	}
	name := strings.TrimPrefix(reference, secretReferenceScheme)
	if filepath.IsAbs(name) || name != filepath.Clean(name) || strings.HasPrefix(name, "..") {
		return nil, unresolved
	}
	file, err := s.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, unresolved
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxSecretDocument {
		return nil, fmt.Errorf("database secret reference must name an owner-only regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("database secret reference must be owned by this user")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSecretDocument+1))
	if err != nil || len(data) > maxSecretDocument {
		return nil, unresolved
	}
	return data, nil
}

// connectionSecret is the private document a credential reference names. It
// carries only the password: endpoint, database and role are catalog
// identity, so rotating a secret can never repoint a target.
type connectionSecret struct {
	Password string `json:"password,omitempty"`
}

// decodeConnectionSecret accepts exactly one JSON object with known,
// non-duplicated keys and nothing after it.
func decodeConnectionSecret(raw []byte) (connectionSecret, error) {
	var secret connectionSecret
	if err := rejectDuplicateKeys(raw); err != nil {
		return secret, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&secret); err != nil {
		return secret, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return secret, fmt.Errorf("trailing data")
	}
	return secret, nil
}

func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("secret must be a JSON object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		name, _ := key.(string)
		if seen[name] {
			return fmt.Errorf("duplicate key")
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	return nil
}

// Session is private connection material for one resolved target. Its
// directory holds the libpq service file and TLS files; Close removes it.
type Session struct {
	target    TargetIdentity
	bindingID string
	directory string
	password  string
	config    *pgx.ConnConfig
	// url is the target as a standard connection URL for local processes;
	// runtimeURL is the same without host-local TLS file paths, empty when
	// the target cannot be delivered to a runtime (see RuntimeConnectionURL).
	url        string
	runtimeURL string
}

// OpenSession builds private material for an application target. Only
// PostgreSQL has an adapter; control-purpose targets are never opened here.
func OpenSession(ctx context.Context, resolved ResolvedBinding, secrets SecretSource) (*Session, error) {
	label := "bindings/" + resolved.Target.BindingID
	if resolved.Target.Engine != EnginePostgreSQL {
		return nil, &ResolverError{Code: CodeUnsupportedEngine, Field: "engine", Resource: label, Reason: "no connection adapter is implemented for this engine"}
	}
	if resolved.Purpose != PurposeApplication {
		return nil, &ResolverError{Code: CodePurposeMismatch, Field: "purpose", Resource: label, Reason: "only application targets are opened by the application adapter"}
	}
	if secrets == nil {
		return nil, &ResolverError{Code: CodeInvalidRequest, Field: "credentialRef", Resource: label, Reason: "no secret source is configured"}
	}
	raw, err := secrets.Resolve(ctx, resolved.CredentialRef)
	if err != nil {
		return nil, &ResolverError{Code: CodeInvalidRequest, Field: "credentialRef", Resource: label, Reason: "credential reference could not be resolved"}
	}
	secret, err := decodeConnectionSecret(raw)
	if err != nil || containsLineBreak(secret.Password) {
		return nil, &ResolverError{Code: CodeInvalidRequest, Field: "credentialRef", Resource: label, Reason: "connection secret must be one strict JSON object containing only an optional password"}
	}
	endpoint := resolved.Endpoint
	if !validEndpointHost(endpoint.Host) || endpoint.Port < 1 || endpoint.Port > 65535 {
		return nil, &ResolverError{Code: CodeInvalidRequest, Field: "endpoint", Resource: label, Reason: "resolved target has no valid catalog endpoint"}
	}
	settings := map[string]string{
		"host": endpoint.Host, "port": strconv.Itoa(endpoint.Port),
		"dbname": resolved.Target.Database, "user": resolved.Target.Role,
	}
	if secret.Password != "" {
		settings["password"] = secret.Password
	}
	directory, err := os.MkdirTemp("", "norn-db-target-*")
	if err != nil {
		return nil, fmt.Errorf("prepare private database material: %w", err)
	}
	session := &Session{target: resolved.Target, bindingID: resolved.Target.BindingID, directory: directory, password: secret.Password}
	fail := func(err error) (*Session, error) {
		_ = session.Close()
		return nil, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fail(err)
	}
	// An empty private passfile stops libpq and pgx from consulting the
	// operator's ~/.pgpass or PGPASSFILE.
	passfile := filepath.Join(directory, "passfile")
	if err := writePrivate(passfile, nil); err != nil {
		return fail(err)
	}
	settings["passfile"] = passfile
	switch resolved.TLS.Mode {
	case TLSDisabled:
		settings["sslmode"] = "disable"
	case TLSVerifyCA, TLSVerifyFull:
		settings["sslmode"] = string(resolved.TLS.Mode)
		if resolved.TLS.Mode == TLSVerifyFull && resolved.TLS.ServerName != endpoint.Host {
			return fail(&ResolverError{Code: CodeInvalidRequest, Field: "tls.serverName", Resource: label, Reason: "verify-full requires the connection host to equal the declared server name"})
		}
		for key, reference := range map[string]string{"sslrootcert": resolved.TLS.CARef, "sslcert": resolved.TLS.ClientCertRef, "sslkey": resolved.TLS.ClientKeyRef} {
			if reference == "" {
				continue
			}
			pem, err := secrets.Resolve(ctx, reference)
			if err != nil {
				return fail(&ResolverError{Code: CodeInvalidRequest, Field: "tls", Resource: label, Reason: "TLS material reference could not be resolved"})
			}
			path := filepath.Join(directory, key+".pem")
			if err := writePrivate(path, pem); err != nil {
				return fail(err)
			}
			settings[key] = path
		}
	default:
		return fail(&ResolverError{Code: CodeInvalidRequest, Field: "tls.mode", Resource: label, Reason: "TLS mode is unsupported"})
	}
	servicePath := filepath.Join(directory, "pg_service.conf")
	if err := writePrivate(servicePath, []byte(serviceFile(settings))); err != nil {
		return fail(err)
	}
	session.url = connectionURL(endpoint, resolved.Target, secret.Password, settings, true)
	if resolved.TLS.Mode == TLSDisabled {
		session.runtimeURL = connectionURL(endpoint, resolved.Target, secret.Password, settings, false)
	}
	if err := writePrivate(filepath.Join(directory, "connection.url"), []byte(session.url)); err != nil {
		return fail(err)
	}
	// pgx reads PG* defaults from the process environment. Every connection
	// setting it would inherit is named explicitly here (empty where unused),
	// including this session's own service file, so no ambient PGSERVICE,
	// PGPASSWORD, PGSSL*, PGOPTIONS or timeout reaches the configuration.
	parse := map[string]string{
		"service": ServiceName, "servicefile": servicePath, "connect_timeout": "10",
		"password": "", "sslrootcert": "", "sslcert": "", "sslkey": "", "sslpassword": "",
		"options": "", "application_name": "norn-database-target", "target_session_attrs": "any",
	}
	for key, value := range settings {
		parse[key] = value
	}
	config, err := pgx.ParseConfig(keywordConnString(parse))
	if err != nil {
		return fail(&ResolverError{Code: CodeInvalidRequest, Field: "credentialRef", Resource: label, Reason: "connection material is invalid"})
	}
	// Belt and braces: pin the fields that carry identity or credentials.
	config.Host, config.Port, config.Database, config.User = endpoint.Host, uint16(endpoint.Port), resolved.Target.Database, resolved.Target.Role
	config.Password = secret.Password
	config.RuntimeParams = map[string]string{"application_name": "norn-database-target"}
	config.ValidateConnect = nil
	config.Fallbacks = nil
	if resolved.TLS.Mode == TLSDisabled {
		config.TLSConfig = nil
	}
	session.config = config
	return session, nil
}

func (s *Session) Target() TargetIdentity { return s.target }

// String and GoString keep diagnostic formatting (%v, %+v, %#v, value or
// pointer) from printing the private password or configuration.
func (s Session) String() string {
	return fmt.Sprintf("database session for binding %s generation %d", s.target.BindingID, s.target.BindingGeneration)
}

func (s Session) GoString() string { return s.String() }

// ServiceArgument is the only value a command needs to select the target.
func (s *Session) ServiceArgument() string { return "service=" + ServiceName }

// Environment is the complete environment for a target command: a small
// allowlist of process basics plus the private service selection. Ambient
// PG*, DATABASE_URL, NORN_* and all other variables are never inherited.
func (s *Session) Environment() []string {
	environment := make([]string, 0, 8)
	for _, name := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR"} {
		if value, ok := os.LookupEnv(name); ok && !containsLineBreak(value) {
			environment = append(environment, name+"="+value)
		}
	}
	return append(environment, "PGSERVICEFILE="+filepath.Join(s.directory, "pg_service.conf"), "PGSERVICE="+ServiceName)
}

// MigrationEnvironment is Environment plus the connection for trusted
// migration code in the forms ordinary clients read: valueEnv receives the
// connection URL itself (node-postgres, Prisma, libpq, pgx and most
// frameworks accept it), and fileEnv receives the path of a private 0600
// file holding that URL. Either name may be empty. libpq tools also keep the
// private service selection from Environment. The process environment is
// private to the child and never logged; its output passes through Redact.
func (s *Session) MigrationEnvironment(valueEnv, fileEnv string) []string {
	environment := s.Environment()
	if valueEnv != "" {
		environment = append(environment, valueEnv+"="+s.url)
	}
	if fileEnv != "" {
		environment = append(environment, fileEnv+"="+filepath.Join(s.directory, "connection.url"))
	}
	return environment
}

// ConnectionURL is the target as a standard postgresql:// URL for local
// processes. It carries the password; callers must never log or persist it.
func (s *Session) ConnectionURL() string { return s.url }

// RuntimeConnectionURL is the value delivered to application processes on
// other hosts. TLS targets are refused until TLS runtime delivery (CA file
// placement in the allocation) is implemented and qualified.
func (s *Session) RuntimeConnectionURL() (string, error) {
	if s.runtimeURL == "" {
		return "", &ResolverError{Code: CodeInvalidRequest, Field: "tls", Resource: "bindings/" + s.bindingID, Reason: "runtime delivery of TLS targets is not implemented; the CA and client files would have to be placed in the allocation"}
	}
	return s.runtimeURL, nil
}

// connectionURL renders a libpq/WHATWG-compatible URL. User and password are
// percent-encoded outside RFC 3986 unreserved characters, so the value is a
// single shell- and env-file-safe token. A socket directory is carried as the
// host query parameter (honoured by libpq and node-postgres) behind a
// placeholder authority, because WHATWG URL parsing rejects credentials with
// an empty host.
func connectionURL(endpoint DatabaseEndpoint, target TargetIdentity, password string, settings map[string]string, localFiles bool) string {
	userinfo := percentEncode(target.Role)
	if password != "" {
		userinfo += ":" + percentEncode(password)
	}
	query := []string{}
	// Hosts are validated DNS names or IP literals, so only IPv6 needs
	// RFC 3986 brackets; nothing in them is percent-encoded.
	host := endpoint.Host
	if address, err := netip.ParseAddr(host); err == nil && address.Is6() {
		host = "[" + host + "]"
	}
	authority := host + ":" + strconv.Itoa(endpoint.Port)
	if strings.HasPrefix(endpoint.Host, "/") {
		authority = "localhost"
		query = append(query, "host="+percentEncode(endpoint.Host), "port="+strconv.Itoa(endpoint.Port))
	}
	query = append(query, "sslmode="+settings["sslmode"])
	if localFiles {
		for _, key := range []string{"sslrootcert", "sslcert", "sslkey"} {
			if settings[key] != "" {
				query = append(query, key+"="+percentEncode(settings[key]))
			}
		}
	}
	return "postgresql://" + userinfo + "@" + authority + "/" + percentEncode(target.Database) + "?" + strings.Join(query, "&")
}

func percentEncode(value string) string {
	var builder strings.Builder
	for _, b := range []byte(value) {
		switch {
		case 'a' <= b && b <= 'z', 'A' <= b && b <= 'Z', '0' <= b && b <= '9', b == '-', b == '.', b == '_', b == '~':
			builder.WriteByte(b)
		default:
			fmt.Fprintf(&builder, "%%%02X", b)
		}
	}
	return builder.String()
}

// Command runs a tool against the target with the private environment.
func (s *Session) Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = s.Environment()
	return command
}

// Redact removes the target password from tool output before it is logged
// or returned in an error.
func (s *Session) Redact(output []byte) string {
	return strings.TrimSpace(s.redact(string(output)))
}

func (s *Session) redact(text string) string {
	if s != nil && s.password != "" {
		text = strings.ReplaceAll(text, s.password, "[redacted]")
		text = strings.ReplaceAll(text, percentEncode(s.password), "[redacted]")
	}
	return text
}

// RedactCaptured redacts bounded command output. Complete secrets in the
// retained head and tail are replaced first. Where output was truncated, a
// secret could also straddle a cut and survive as a fragment (shorter than
// the secret) that replacement cannot match, so that many bytes at each cut
// are then dropped as well.
func (s *Session) RedactCaptured(output *capture.Buffer) string {
	head, tail, dropped := output.Parts()
	redactedHead, redactedTail := s.redact(string(head)), s.redact(string(tail))
	if dropped > 0 && s != nil && s.password != "" {
		margin := max(len(s.password), len(percentEncode(s.password))) - 1
		cut := min(margin, len(redactedHead))
		redactedHead = redactedHead[:len(redactedHead)-cut]
		skip := min(margin, len(redactedTail))
		redactedTail = redactedTail[skip:]
		dropped += int64(cut + skip)
	}
	return strings.TrimSpace(capture.Join([]byte(redactedHead), []byte(redactedTail), dropped))
}

type ProbeResult struct {
	Database      string `json:"database"`
	Role          string `json:"role"`
	ServerVersion string `json:"serverVersion"`
}

// Probe connects in-process and proves the session reaches the declared
// database as the declared role.
func (s *Session) Probe(ctx context.Context) (ProbeResult, error) {
	if s == nil || s.config == nil {
		return ProbeResult{}, fmt.Errorf("database target session is closed")
	}
	connection, err := pgx.ConnectConfig(ctx, s.config.Copy())
	if err != nil {
		return ProbeResult{}, s.probeError("connect", err)
	}
	defer connection.Close(context.Background())
	var result ProbeResult
	if err := connection.QueryRow(ctx, `SELECT current_database(), current_user, current_setting('server_version')`).Scan(&result.Database, &result.Role, &result.ServerVersion); err != nil {
		return ProbeResult{}, s.probeError("identify", err)
	}
	if result.Database != s.target.Database || result.Role != s.target.Role {
		return result, &ResolverError{Code: CodeStaleTarget, Field: "probe", Resource: "bindings/" + s.bindingID, Reason: "connected database or role differs from the declared target"}
	}
	return result, nil
}

func (s *Session) probeError(stage string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("database target %s %s failed (SQLSTATE %s)", s.bindingID, stage, pgErr.Code)
	}
	return fmt.Errorf("database target %s %s failed", s.bindingID, stage)
}

func (s *Session) Close() error {
	if s == nil || s.directory == "" {
		return nil
	}
	err := os.RemoveAll(s.directory)
	s.directory, s.config, s.password, s.url, s.runtimeURL = "", nil, "", "", ""
	return err
}

func writePrivate(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func serviceFile(settings map[string]string) string {
	var builder strings.Builder
	builder.WriteString("[" + ServiceName + "]\n")
	for _, key := range sortedKeys(settings) {
		builder.WriteString(key + "=" + settings[key] + "\n")
	}
	return builder.String()
}

func keywordConnString(settings map[string]string) string {
	parts := make([]string, 0, len(settings))
	for _, key := range sortedKeys(settings) {
		value := strings.ReplaceAll(strings.ReplaceAll(settings[key], `\`, `\\`), `'`, `\'`)
		parts = append(parts, key+"='"+value+"'")
	}
	return strings.Join(parts, " ")
}

func containsLineBreak(values ...string) bool {
	for _, value := range values {
		if strings.ContainsAny(value, "\x00\r\n") {
			return true
		}
	}
	return false
}
