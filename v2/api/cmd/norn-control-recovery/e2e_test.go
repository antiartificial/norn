package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sys/unix"

	"norn/v2/api/controlrecovery"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

const cliCanary = "NORN_CLI_RECOVERY_CANARY_93b1"

// TestMain lets tests execute the real command in a child process, so flag
// parsing, private-file checks, stdout/stderr and exit status are exercised.
func TestMain(m *testing.M) {
	if os.Getenv("NORN_CONTROL_RECOVERY_TEST_EXEC") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type cliResult struct {
	stdout, stderr []byte
	err            error
}

func runCLI(t *testing.T, arguments ...string) cliResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], arguments...)
	command.Env = append(os.Environ(), "NORN_CONTROL_RECOVERY_TEST_EXEC=1")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return cliResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), err: err}
}

func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) string {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func encryptTo(t *testing.T, recipient age.Recipient, plaintext []byte) []byte {
	t.Helper()
	var encrypted bytes.Buffer
	writer, err := age.Encrypt(&encrypted, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(plaintext); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return encrypted.Bytes()
}

func TestPublicAndPrivateReadersRejectFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan [2]error, 1)
	go func() {
		_, publicErr := readBoundedRegularFile(path, 1024)
		_, privateErr := readPrivateBytes(path)
		done <- [2]error{publicErr, privateErr}
	}()
	select {
	case errs := <-done:
		if errs[0] == nil || errs[1] == nil {
			t.Fatalf("FIFO accepted: public=%v private=%v", errs[0], errs[1])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO input blocked")
	}
	if _, err := readBoundedRegularFile("", 1024); err == nil {
		t.Fatal("empty public input path accepted")
	}
	large := writeFile(t, filepath.Join(t.TempDir(), "large"), bytes.Repeat([]byte{'x'}, 2048), 0o644)
	if _, err := readBoundedRegularFile(large, 1024); err == nil {
		t.Fatal("oversized public input accepted")
	}
}

func TestCLIRejectsUnencryptedOrMalformedRecoveryKeyDocuments(t *testing.T) {
	directory := t.TempDir()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	identityFile := writeFile(t, filepath.Join(directory, "identity"), []byte(identity.String()+"\n"), 0o600)
	publicFile := writeFile(t, filepath.Join(directory, "manifest.pub"), []byte(base64.RawStdEncoding.EncodeToString(public)), 0o644)
	bundle := writeFile(t, filepath.Join(directory, "bundle.age"), []byte("not a bundle"), 0o600)
	key := base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("k", 24) + cliCanary))
	for name, document := range map[string][]byte{
		"plaintext":     []byte(`{"hmacKeys":["` + key + `"]}`),
		"unknown field": encryptTo(t, identity.Recipient(), []byte(`{"hmacKeys":["`+key+`"],"extra":true}`)),
		"short key":     encryptTo(t, identity.Recipient(), []byte(`{"hmacKeys":["`+base64.RawStdEncoding.EncodeToString([]byte("short"))+`"]}`)),
		"padded base64": encryptTo(t, identity.Recipient(), []byte(`{"hmacKeys":["`+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 31)))+`"]}`)),
		"trailing json": encryptTo(t, identity.Recipient(), []byte(`{"hmacKeys":["`+key+`"]} {}`)),
		"bad public":    encryptTo(t, identity.Recipient(), []byte(`{"qualificationPublicKeys":["`+cliCanary+`"]}`)),
	} {
		t.Run(name, func(t *testing.T) {
			keys := writeFile(t, filepath.Join(t.TempDir(), "recovery-keys.age"), document, 0o600)
			result := runCLI(t, "bundle-verify", "--bundle", bundle, "--identity-file", identityFile, "--manifest-public-key-file", publicFile, "--recovery-keys-file", keys)
			if result.err == nil {
				t.Fatal("malformed recovery key document accepted")
			}
			if bytes.Contains(result.stderr, []byte(cliCanary)) || bytes.Contains(result.stdout, []byte(cliCanary)) {
				t.Fatalf("recovery key material leaked: %s %s", result.stdout, result.stderr)
			}
		})
	}
	groupReadable := writeFile(t, filepath.Join(directory, "identity-open"), []byte(identity.String()+"\n"), 0o640)
	if result := runCLI(t, "bundle-verify", "--bundle", bundle, "--identity-file", groupReadable, "--manifest-public-key-file", publicFile); result.err == nil {
		t.Fatal("group-readable age identity accepted")
	}
}

func TestCLIEncryptedRecoveryKeysAndPassiveRestoreEndToEnd(t *testing.T) {
	sourceURL := os.Getenv("NORN_TEST_DATABASE_URL")
	targetURL := os.Getenv("NORN_TEST_RECOVERY_TARGET_DATABASE_URL")
	if sourceURL == "" || targetURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL and NORN_TEST_RECOVERY_TARGET_DATABASE_URL are required")
	}
	ctx := context.Background()
	schema := "norn_cli_recovery_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	sourceAdmin, err := pgxpool.New(ctx, sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	targetAdmin, err := pgxpool.New(ctx, targetURL)
	if err != nil {
		sourceAdmin.Close()
		t.Fatal(err)
	}
	if _, err := sourceAdmin.Exec(ctx, `CREATE SCHEMA `+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = sourceAdmin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+identifier+` CASCADE`)
		_, _ = targetAdmin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+identifier+` CASCADE`)
		sourceAdmin.Close()
		targetAdmin.Close()
	})
	config, err := pgxpool.ParseConfig(sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	source, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	migrator, err := store.NewControlSchemaMigrator(&store.DB{Pool: source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	hmacKey := "cli-recovery-hmac-" + cliCanary
	signer, err := store.NewHMACAcceptanceSigner(hmacKey)
	if err != nil {
		t.Fatal(err)
	}
	operationStore, err := store.NewPGOperationStore(&store.DB{Pool: source}, signer, store.AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := operationStore.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	acceptance := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "cli-test", Subject: "operator"}, Kind: "app.restart", Resource: "cli-app", Key: "request-" + cliCanary},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "app.restart", App: "cli-app", Status: model.OperationQueued, Source: "cli-test", Payload: map[string]interface{}{"app": "cli-app"}, Metadata: map[string]interface{}{}},
		Audit:     store.AcceptanceAuditContext{Source: "cli-test"},
	}
	if acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance); err != nil {
		t.Fatal(err)
	}
	if _, err := operationStore.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}

	directory := t.TempDir()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	manifestPublic, manifestPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sourceFile := writeFile(t, filepath.Join(directory, "source.url"), []byte(sourceURL+"\n"), 0o600)
	targetFile := writeFile(t, filepath.Join(directory, "target.url"), []byte(targetURL+"\n"), 0o600)
	identityFile := writeFile(t, filepath.Join(directory, "identity"), []byte(identity.String()+"\n"), 0o600)
	recipientsFile := writeFile(t, filepath.Join(directory, "recipients"), []byte(identity.Recipient().String()+"\n"), 0o644)
	signingFile := writeFile(t, filepath.Join(directory, "manifest.key"), []byte(base64.RawStdEncoding.EncodeToString(manifestPrivate)), 0o600)
	publicFile := writeFile(t, filepath.Join(directory, "manifest.pub"), []byte(base64.RawStdEncoding.EncodeToString(manifestPublic)), 0o644)
	document := []byte(`{"hmacKeys":["` + base64.RawStdEncoding.EncodeToString([]byte(hmacKey)) + `"],"qualificationPublicKeys":[]}`)
	keysFile := writeFile(t, filepath.Join(directory, "recovery-keys.age"), encryptTo(t, identity.Recipient(), document), 0o600)
	wrongDocument := []byte(`{"hmacKeys":["` + base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("w", 40))) + `"]}`)
	wrongKeysFile := writeFile(t, filepath.Join(directory, "wrong-keys.age"), encryptTo(t, identity.Recipient(), wrongDocument), 0o600)
	plainKeysFile := writeFile(t, filepath.Join(directory, "plain-keys.json"), document, 0o600)
	bundle := filepath.Join(directory, "control.age")
	inspection := filepath.Join(directory, "inspection.json")

	var outputs [][]byte
	expectSuccess := func(arguments ...string) []byte {
		t.Helper()
		result := runCLI(t, arguments...)
		outputs = append(outputs, result.stdout, result.stderr)
		if result.err != nil {
			t.Fatalf("%s failed: %v\n%s", arguments[0], result.err, result.stderr)
		}
		return result.stdout
	}
	expectFailure := func(arguments ...string) {
		t.Helper()
		result := runCLI(t, arguments...)
		outputs = append(outputs, result.stdout, result.stderr)
		if result.err == nil {
			t.Fatalf("%s unexpectedly succeeded: %s", arguments[0], result.stdout)
		}
		var exitErr *exec.ExitError
		if !errors.As(result.err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("%s did not exit 1: %v", arguments[0], result.err)
		}
	}
	targetSchemaExists := func() bool {
		var exists bool
		if err := targetAdmin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		return exists
	}

	expectSuccess("inspect", "--database-url-file", sourceFile, "--schema", schema, "--output", inspection)
	info, err := os.Stat(inspection)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("inspection output mode = %v, %v", info, err)
	}
	inspected, err := os.ReadFile(inspection)
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, inspected)
	expectFailure("inspect", "--database-url-file", sourceFile, "--schema", schema, "--output", inspection)

	var manifest controlrecovery.RecoveryManifest
	if err := json.Unmarshal(expectSuccess("bundle-create", "--database-url-file", sourceFile, "--schema", schema, "--output", bundle, "--recipients-file", recipientsFile, "--manifest-signing-key-file", signingFile), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != schema || len(manifest.RequiredSigningKeyIDs) != 1 || manifest.UnresolvedEffects != 0 {
		t.Fatalf("manifest = %#v", manifest)
	}
	ciphertext, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, ciphertext)

	verifyArgs := []string{"--bundle", bundle, "--identity-file", identityFile, "--manifest-public-key-file", publicFile}
	expectFailure(append([]string{"bundle-verify"}, verifyArgs...)...)
	expectSuccess(append([]string{"bundle-verify", "--recovery-keys-file", keysFile}, verifyArgs...)...)

	restoreArgs := append([]string{"--target-database-url-file", targetFile}, verifyArgs...)
	expectFailure(append([]string{"restore-passive", "--isolated-target", "--recovery-keys-file", plainKeysFile}, restoreArgs...)...)
	expectFailure(append([]string{"restore-passive", "--isolated-target", "--recovery-keys-file", wrongKeysFile}, restoreArgs...)...)
	expectFailure(append([]string{"restore-passive", "--isolated-target"}, restoreArgs...)...)
	expectFailure(append([]string{"restore-passive", "--recovery-keys-file", keysFile}, restoreArgs...)...)
	if targetSchemaExists() {
		t.Fatal("rejected restore invocation wrote the target")
	}

	var report controlrecovery.RestoreReport
	if err := json.Unmarshal(expectSuccess(append([]string{"restore-passive", "--isolated-target", "--recovery-keys-file", keysFile}, restoreArgs...)...), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Passive || report.ActivationReady || report.BundleID != manifest.BundleID || report.Authority != authority || report.UnresolvedEffects != 0 {
		t.Fatalf("restore report = %#v", report)
	}
	var restored int
	if err := targetAdmin.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{schema, "operation_acceptance_intents"}.Sanitize()).Scan(&restored); err != nil || restored != 1 {
		t.Fatalf("restored intents = %d, %v", restored, err)
	}
	expectFailure(append([]string{"restore-passive", "--isolated-target", "--recovery-keys-file", keysFile}, restoreArgs...)...)

	for _, output := range outputs {
		for _, secret := range []string{cliCanary, sourceURL, targetURL} {
			if bytes.Contains(output, []byte(secret)) {
				t.Fatalf("CLI output or artifact exposed a secret: %q", secret)
			}
		}
	}
}
