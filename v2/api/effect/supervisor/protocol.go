package supervisor

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"norn/v2/api/effect"
)

const ProtocolV1 = "norn.effect-runner/v1"

// Descriptor is the only launch material persisted in the control database.
// It intentionally contains hashes and environment variable names, never the
// shell text, working directory, or environment values used for launch.
type Descriptor struct {
	Protocol         string   `json:"protocol"`
	SupervisorRootID string   `json:"supervisorRootId"`
	Stage            string   `json:"stage"`
	MaterialMAC      string   `json:"materialMac"`
	EnvironmentNames []string `json:"environmentNames,omitempty"`
}

func (m *Manager) BuildTestDescriptor(material effect.LaunchMaterial) (json.RawMessage, error) {
	if m == nil || m.rootID == "" {
		return nil, fmt.Errorf("effect supervisor manager is unavailable")
	}
	if err := validateBuildTestMaterial(material); err != nil {
		return nil, err
	}
	names, err := environmentNames(material.Environment)
	if err != nil {
		return nil, err
	}
	descriptor := Descriptor{
		Protocol:         ProtocolV1,
		SupervisorRootID: m.rootID,
		Stage:            "build.test",
		MaterialMAC:      m.materialMAC(material),
		EnvironmentNames: names,
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return nil, fmt.Errorf("encode effect runner descriptor: %w", err)
	}
	return encoded, nil
}

func (m *Manager) verifyDescriptor(payload json.RawMessage, material *effect.LaunchMaterial) (Descriptor, error) {
	var descriptor Descriptor
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return Descriptor{}, fmt.Errorf("decode effect runner descriptor: %w", err)
	}
	if descriptor.Protocol != ProtocolV1 || descriptor.SupervisorRootID != m.rootID || descriptor.Stage != "build.test" {
		return Descriptor{}, fmt.Errorf("unsupported effect runner descriptor")
	}
	if material != nil {
		if err := validateBuildTestMaterial(*material); err != nil {
			return Descriptor{}, err
		}
		names, err := environmentNames(material.Environment)
		if err != nil {
			return Descriptor{}, err
		}
		if descriptor.MaterialMAC != m.materialMAC(*material) || !equalStrings(descriptor.EnvironmentNames, names) {
			return Descriptor{}, fmt.Errorf("effect launch material does not match its persisted descriptor")
		}
	}
	return descriptor, nil
}

// MaxBuildTestTimeout bounds a single supervised build.test execution.
const MaxBuildTestTimeout = 24 * time.Hour

func validateBuildTestMaterial(material effect.LaunchMaterial) error {
	if len(material.Argv) != 3 || material.Argv[0] != "sh" || material.Argv[1] != "-c" || strings.TrimSpace(material.Argv[2]) == "" {
		return fmt.Errorf("build.test launch requires the vetted sh -c command shape")
	}
	if !filepath.IsAbs(material.Directory) {
		return fmt.Errorf("build.test working directory must be absolute")
	}
	if strings.TrimSpace(material.Subject) == "" || len(material.Subject) > 512 || strings.ContainsAny(material.Subject, "\x00\r\n") {
		return fmt.Errorf("build.test subject identity is required")
	}
	if material.Timeout <= 0 || material.Timeout > MaxBuildTestTimeout {
		return fmt.Errorf("build.test timeout must be positive and at most %s", MaxBuildTestTimeout)
	}
	return nil
}

// materialMAC binds the command, environment values, subject and timeout.
// The working directory is deliberately excluded: each claim prepares its own
// checkout, and the subject (pinned commit) identifies the tree instead.
func (m *Manager) materialMAC(material effect.LaunchMaterial) string {
	encoded, _ := json.Marshal(struct {
		Protocol      string   `json:"protocol"`
		Stage         string   `json:"stage"`
		Argv          []string `json:"argv"`
		Environment   []string `json:"environment"`
		Subject       string   `json:"subject"`
		TimeoutMillis int64    `json:"timeoutMillis"`
	}{Protocol: ProtocolV1, Stage: "build.test", Argv: material.Argv, Environment: material.Environment, Subject: material.Subject, TimeoutMillis: material.Timeout.Milliseconds()})
	return m.mac(encoded)
}

func environmentNames(environment []string) ([]string, error) {
	names := make([]string, 0, len(environment))
	seen := map[string]struct{}{}
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\x00\r\n") {
			return nil, fmt.Errorf("invalid effect environment entry")
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate effect environment variable %q", name)
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
