package model

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type DeployGroup struct {
	Name string           `yaml:"name" json:"name"`
	Apps []DeployGroupApp `yaml:"apps" json:"apps"`
}

type DeployGroupApp struct {
	App       string `yaml:"app" json:"app"`
	WaitReady bool   `yaml:"waitReady,omitempty" json:"waitReady,omitempty"`
}

func LoadDeployGroup(path string) (*DeployGroup, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g DeployGroup
	if err := yaml.Unmarshal(data, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

func DiscoverDeployGroups(appsDir string) ([]*DeployGroup, error) {
	groupsDir := filepath.Join(appsDir, "deploy-groups")
	entries, err := os.ReadDir(groupsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read deploy-groups dir: %w", err)
	}
	var groups []*DeployGroup
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := filepath.Ext(entry.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		g, err := LoadDeployGroup(filepath.Join(groupsDir, entry.Name()))
		if err != nil {
			continue
		}
		if g.Name == "" {
			stem := entry.Name()[:len(entry.Name())-len(ext)]
			g.Name = stem
		}
		groups = append(groups, g)
	}
	return groups, nil
}
