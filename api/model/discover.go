package model

import (
	"os"
	"path/filepath"
)

// DiscoverApps scans the given directory for subdirectories containing
// infraspec.yaml and returns the parsed specs.
func DiscoverApps(appsDir string) ([]*InfraSpec, error) {
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		return nil, err
	}

	var specs []*InfraSpec
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		specPath := filepath.Join(appsDir, entry.Name(), "infraspec.yaml")
		spec, err := LoadInfraSpec(specPath)
		if err != nil {
			continue
		}
		if !spec.Deploy {
			continue
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
