package model

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DiscoverApps scans the given directory for subdirectories containing
// infraspec.yaml with deploy: true.
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

// FindByRepoURL finds an app spec matching the given repo URL and branch
// that has autoDeploy enabled.
func FindByRepoURL(specs []*InfraSpec, repoURL, branch string) *InfraSpec {
	incoming := normalizeRepoPath(repoURL)
	for _, s := range specs {
		if s.Repo == nil || !s.Repo.AutoDeploy {
			continue
		}
		specBranch := s.Repo.Branch
		if specBranch == "" {
			specBranch = "main"
		}
		if specBranch != branch {
			continue
		}
		if normalizeRepoPath(s.Repo.URL) == incoming {
			return s
		}
	}
	return nil
}

// normalizeRepoPath extracts the owner/repo path from a git URL,
// stripping .git suffix, for comparison.
func normalizeRepoPath(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)

	// Handle SSH URLs like git@github.com:owner/repo.git
	if strings.HasPrefix(rawURL, "git@") {
		if idx := strings.Index(rawURL, ":"); idx != -1 {
			path := rawURL[idx+1:]
			path = strings.TrimSuffix(path, ".git")
			return strings.ToLower(path)
		}
	}

	// Parse as URL
	u, err := url.Parse(rawURL)
	if err != nil {
		return strings.ToLower(strings.TrimSuffix(rawURL, ".git"))
	}

	path := strings.TrimPrefix(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	return strings.ToLower(path)
}
