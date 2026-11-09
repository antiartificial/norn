package secrets

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Manager handles SOPS-encrypted secret files.
type Manager struct {
	appsDir string
}

func NewManager(appsDir string) *Manager {
	return &Manager{appsDir: appsDir}
}

func (m *Manager) SecretFile(appID string) string {
	return filepath.Join(m.appsDir, appID, "secrets.enc.yaml")
}

func (m *Manager) List(appID string) ([]string, error) {
	data, err := m.decrypt(appID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	return keys, nil
}

func (m *Manager) Get(appID string) (map[string]string, error) {
	return m.decrypt(appID)
}

func (m *Manager) Set(appID string, updates map[string]string) error {
	existing, err := m.decrypt(appID)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("decrypt: %w", err)
	}
	if existing == nil {
		existing = make(map[string]string)
	}
	for k, v := range updates {
		existing[k] = v
	}
	return m.encrypt(appID, existing)
}

func (m *Manager) Delete(appID string, key string) error {
	existing, err := m.decrypt(appID)
	if err != nil {
		return err
	}
	delete(existing, key)
	return m.encrypt(appID, existing)
}

// EnvMap decrypts secrets and returns them as env var entries (KEY=VALUE).
func (m *Manager) EnvMap(appID string) (map[string]string, error) {
	data, err := m.decrypt(appID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// DecryptSOPSSnapshot decrypts the exact encrypted YAML bytes retained by a
// workload release. It sends the ciphertext through stdin and keeps plaintext
// in memory only; callers must clear the returned values after staging them.
func DecryptSOPSSnapshot(ciphertext []byte) (map[string]string, error) {
	return decryptSOPSSnapshotWithCommand(ciphertext, "sops")
}

func decryptSOPSSnapshotWithCommand(ciphertext []byte, command string) (map[string]string, error) {
	if len(ciphertext) == 0 || command == "" {
		return nil, fmt.Errorf("encrypted SOPS snapshot is unavailable")
	}
	cmd := exec.Command(command, "decrypt", "--filename-override", "secrets.enc.yaml", "--input-type", "yaml", "--output-type", "yaml")
	cmd.Stdin = bytes.NewReader(ciphertext)
	plain, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("decrypt retained SOPS snapshot")
	}
	defer clear(plain)
	var values map[string]string
	if err := yaml.Unmarshal(plain, &values); err != nil || values == nil {
		return nil, fmt.Errorf("retained SOPS snapshot is not a string map")
	}
	return values, nil
}

func (m *Manager) decrypt(appID string) (map[string]string, error) {
	path := m.SecretFile(appID)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, err
	}

	cmd := exec.Command("sops", "--decrypt", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sops decrypt: %w", err)
	}

	var data map[string]string
	if err := yaml.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("unmarshal secrets: %w", err)
	}
	return data, nil
}

func (m *Manager) encrypt(appID string, data map[string]string) error {
	path := m.SecretFile(appID)

	plain, err := yaml.Marshal(data)
	if err != nil {
		return err
	}
	defer clear(plain)

	configFile := filepath.Join(m.appsDir, appID, ".sops.yaml")
	cmd := exec.Command("sops", "--config", configFile, "--encrypt", "--input-type", "yaml", "--output-type", "yaml", "/dev/stdin")
	cmd.Stdin = bytes.NewReader(plain)
	encrypted, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("sops encrypt: %s", string(exitErr.Stderr))
		}
		return fmt.Errorf("sops encrypt: %w", err)
	}

	return writeEncryptedSecretAtomically(path, encrypted)
}

func writeEncryptedSecretAtomically(path string, encrypted []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".secrets-encrypted-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(encrypted); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
