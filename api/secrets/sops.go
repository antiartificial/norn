package secrets

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Manager handles SOPS-encrypted secret files and syncs them to K8s secrets.
type Manager struct {
	appsDir string
}

func NewManager(appsDir string) *Manager {
	return &Manager{appsDir: appsDir}
}

// SecretFile returns the path to an app's encrypted secrets file.
func (m *Manager) SecretFile(appID string) string {
	return filepath.Join(m.appsDir, appID, "secrets.enc.yaml")
}

// List returns secret key names (not values) from the encrypted file.
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

// Get decrypts and returns all secret key-value pairs for an app.
func (m *Manager) Get(appID string) (map[string]string, error) {
	return m.decrypt(appID)
}

// Set updates or adds secrets, re-encrypts, and writes back.
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

// Delete removes a secret key, re-encrypts, and writes back.
func (m *Manager) Delete(appID string, key string) error {
	existing, err := m.decrypt(appID)
	if err != nil {
		return err
	}
	delete(existing, key)
	return m.encrypt(appID, existing)
}

// SyncToK8s creates or updates a K8s secret from the decrypted values.
func (m *Manager) SyncToK8s(ctx context.Context, appID, namespace string) error {
	data, err := m.decrypt(appID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	if len(data) == 0 {
		return nil
	}

	args := []string{
		"-n", namespace, "create", "secret", "generic",
		appID + "-secrets",
		"--dry-run=client", "-o", "yaml",
	}
	for k, v := range data {
		args = append(args, fmt.Sprintf("--from-literal=%s=%s", k, v))
	}

	create := exec.CommandContext(ctx, "kubectl", args...)
	out, err := create.Output()
	if err != nil {
		return fmt.Errorf("kubectl create secret: %w", err)
	}

	apply := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	apply.Stdin = bytes.NewReader(out)
	if applyOut, err := apply.CombinedOutput(); err != nil {
		return fmt.Errorf("kubectl apply: %s: %w", string(applyOut), err)
	}

	return nil
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

	tmpFile := path + ".tmp"
	if err := os.WriteFile(tmpFile, plain, 0600); err != nil {
		return err
	}
	defer os.Remove(tmpFile)

	cmd := exec.Command("sops", "--encrypt", "--input-type", "yaml", "--output-type", "yaml", tmpFile)
	encrypted, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("sops encrypt: %s", string(exitErr.Stderr))
		}
		return fmt.Errorf("sops encrypt: %w", err)
	}

	return os.WriteFile(path, encrypted, 0600)
}
