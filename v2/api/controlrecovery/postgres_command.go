package controlrecovery

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var safeSchemaName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

type libpqService struct {
	environment []string
	directory   string
}

func newLibpqService(databaseURL string) (*libpqService, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.User == nil {
		return nil, fmt.Errorf("control recovery database URL must be a PostgreSQL URL")
	}
	values := map[string]string{
		"host":   parsed.Hostname(),
		"port":   parsed.Port(),
		"dbname": strings.TrimPrefix(parsed.EscapedPath(), "/"),
		"user":   parsed.User.Username(),
	}
	if password, present := parsed.User.Password(); present {
		values["password"] = password
	}
	allowedQuery := map[string]bool{
		"host": true, "port": true, "sslmode": true, "sslrootcert": true, "sslcert": true,
		"sslkey": true, "connect_timeout": true, "target_session_attrs": true,
	}
	for key, list := range parsed.Query() {
		if !allowedQuery[key] || len(list) != 1 {
			return nil, fmt.Errorf("control recovery database URL contains unsupported connection option")
		}
		values[key] = list[0]
	}
	decodedDatabase, err := url.PathUnescape(values["dbname"])
	if err != nil || decodedDatabase == "" {
		return nil, fmt.Errorf("control recovery database URL has invalid database name")
	}
	values["dbname"] = decodedDatabase
	for _, value := range values {
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("control recovery database URL contains unsafe connection value")
		}
	}
	directory, err := os.MkdirTemp("", "norn-control-recovery-libpq-*")
	if err != nil {
		return nil, fmt.Errorf("control recovery private connection setup failed")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		return nil, fmt.Errorf("control recovery private connection permission failed")
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var config strings.Builder
	config.WriteString("[norn_recovery]\n")
	for _, key := range keys {
		config.WriteString(key)
		config.WriteByte('=')
		config.WriteString(values[key])
		config.WriteByte('\n')
	}
	path := filepath.Join(directory, "pg_service.conf")
	if err := os.WriteFile(path, []byte(config.String()), 0o600); err != nil {
		_ = os.RemoveAll(directory)
		return nil, fmt.Errorf("control recovery private connection write failed")
	}
	environment := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(name, "PG") {
			continue
		}
		environment = append(environment, value)
	}
	environment = append(environment, "PGSERVICEFILE="+path, "PGSERVICE=norn_recovery")
	return &libpqService{environment: environment, directory: directory}, nil
}

func (s *libpqService) Close() error {
	if s == nil || s.directory == "" {
		return nil
	}
	err := os.RemoveAll(s.directory)
	s.directory = ""
	return err
}
