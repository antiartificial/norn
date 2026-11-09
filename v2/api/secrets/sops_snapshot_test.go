package secrets

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecryptSOPSSnapshotStreamsCiphertextAndReturnsStringMap(t *testing.T) {
	command := writeSnapshotSOPSStub(t, `#!/bin/sh
[ "$1" = "decrypt" ] || exit 10
[ "$2" = "--filename-override" ] || exit 11
[ "$3" = "secrets.enc.yaml" ] || exit 12
[ "$4" = "--input-type" ] || exit 13
[ "$5" = "yaml" ] || exit 14
[ "$6" = "--output-type" ] || exit 15
[ "$7" = "yaml" ] || exit 16
[ "$(cat)" = "ciphertext-fixture" ] || exit 15
printf 'TOKEN: private-value\nUNRELATED: also-private\n'
`)
	values, err := decryptSOPSSnapshotWithCommand([]byte("ciphertext-fixture"), command)
	if err != nil || values["TOKEN"] != "private-value" || values["UNRELATED"] != "also-private" {
		t.Fatalf("values=%v err=%v", values, err)
	}
}

func TestDecryptSOPSSnapshotFailsWithoutLeakingCommandOutput(t *testing.T) {
	command := writeSnapshotSOPSStub(t, "#!/bin/sh\necho private-error-marker >&2\nexit 1\n")
	if _, err := decryptSOPSSnapshotWithCommand([]byte("ciphertext-fixture"), command); err == nil || strings.Contains(err.Error(), "private-error-marker") {
		t.Fatalf("decrypt error exposed subprocess output: %v", err)
	}
	if _, err := DecryptSOPSSnapshot(nil); err == nil {
		t.Fatal("empty ciphertext was accepted")
	}
}

func TestDecryptSOPSSnapshotRejectsNonStringOrMalformedYAML(t *testing.T) {
	for _, output := range []string{"TOKEN: [not-a-string]", ": malformed"} {
		command := writeSnapshotSOPSStub(t, "#!/bin/sh\ncat >/dev/null\nprintf '%s' '"+output+"'\n")
		if _, err := decryptSOPSSnapshotWithCommand([]byte("ciphertext-fixture"), command); err == nil {
			t.Fatalf("invalid plaintext YAML %q was accepted", output)
		}
	}
}

func TestDecryptSOPSSnapshotWithSOPSCLI(t *testing.T) {
	sopsPath, sopsErr := exec.LookPath("sops")
	ageKeygenPath, ageErr := exec.LookPath("age-keygen")
	if sopsErr != nil || ageErr != nil {
		t.Skip("sops and age-keygen are required for the local round-trip check")
	}
	root := t.TempDir()
	keyPath := filepath.Join(root, "age-key")
	keygen := exec.Command(ageKeygenPath, "-o", keyPath)
	if output, err := keygen.CombinedOutput(); err != nil {
		t.Fatalf("generate ephemeral age key: %v (%s)", err, output)
	}
	publicKey, err := exec.Command(ageKeygenPath, "-y", keyPath).Output()
	if err != nil {
		t.Fatalf("read ephemeral age recipient: %v", err)
	}
	plain := []byte("TOKEN: exact-snapshot-value\nUNRELATED: extra-value\n")
	encrypt := exec.Command(sopsPath, "encrypt", "--filename-override", "secrets.enc.yaml", "--input-type", "yaml", "--output-type", "yaml", "--age", strings.TrimSpace(string(publicKey)))
	encrypt.Stdin = bytes.NewReader(plain)
	ciphertext, err := encrypt.Output()
	clear(plain)
	if err != nil {
		t.Fatalf("encrypt synthetic SOPS snapshot: %v", err)
	}
	defer clear(ciphertext)
	t.Setenv("SOPS_AGE_KEY_FILE", keyPath)
	values, err := DecryptSOPSSnapshot(ciphertext)
	if err != nil || values["TOKEN"] != "exact-snapshot-value" || values["UNRELATED"] != "extra-value" {
		t.Fatalf("decrypted values did not match synthetic snapshot: err=%v", err)
	}
}

func writeSnapshotSOPSStub(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sops")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
