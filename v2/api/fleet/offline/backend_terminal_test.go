package offline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

type terminalBackendFixture struct {
	bound, post               []byte
	boundVersion, postVersion string
	latest                    []string
	versioning                bool
	locked                    bool
	latestCalls               int
}

func (f *terminalBackendFixture) VersioningEnabled(context.Context, string, string) (bool, error) {
	return f.versioning, nil
}
func (f *terminalBackendFixture) Latest(context.Context, string, string) (string, int64, error) {
	if f.latestCalls >= len(f.latest) {
		return "", 0, ErrLatestBackendMismatch
	}
	v := f.latest[f.latestCalls]
	f.latestCalls++
	return v, int64(len(f.post)), nil
}
func (f *terminalBackendFixture) VersionSize(_ context.Context, _, _, version string) (int64, error) {
	if version != f.boundVersion {
		return 0, ErrLatestBackendMismatch
	}
	return int64(len(f.bound)), nil
}
func (f *terminalBackendFixture) Version(_ context.Context, _, _, version string, size int64) ([]byte, error) {
	var raw []byte
	if version == f.boundVersion {
		raw = f.bound
	} else if version == f.postVersion {
		raw = f.post
	} else {
		return nil, ErrLatestBackendMismatch
	}
	if int64(len(raw)) != size {
		return nil, ErrLatestBackendMismatch
	}
	return append([]byte(nil), raw...), nil
}
func (f *terminalBackendFixture) ForeignLockAbsent(context.Context, string, string) error {
	if f.locked {
		return ErrLatestBackendMismatch
	}
	return nil
}

func terminalStateRaw(t *testing.T, binding Binding, serial uint64, omitTarget bool) []byte {
	t.Helper()
	instances := make([]any, 0, len(binding.ProviderNodes))
	members := []string{"do:loadbalancer:stable-lb"}
	for _, node := range binding.ProviderNodes {
		if omitTarget && node.Name == binding.NodeName {
			continue
		}
		members = append(members, "do:droplet:"+node.ProviderID)
		instances = append(instances, map[string]any{"index_key": node.Name, "attributes": map[string]any{"id": node.ProviderID, "name": node.Name, "vpc_uuid": workVPCUUID, "size": "s-2vcpu-4gb", "tags": []string{"norn-work-staging"}}})
	}
	state := map[string]any{"version": 4, "terraform_version": "1.10.6", "lineage": binding.Backend.Lineage, "serial": serial, "resources": []any{
		map[string]any{"mode": "managed", "type": "digitalocean_project_resources", "name": "work", "provider": `provider["registry.opentofu.org/digitalocean/digitalocean"]`, "instances": []any{map[string]any{"attributes": map[string]any{"id": "membership-id", "project": "work-project", "resources": members}}}},
		map[string]any{"mode": "managed", "type": "digitalocean_droplet", "name": "nodes", "provider": `provider["registry.opentofu.org/digitalocean/digitalocean"]`, "instances": instances},
	}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func terminalBackendSetup(t *testing.T) (Binding, *terminalBackendFixture) {
	t.Helper()
	binding, _, _, _ := deleteInputs(t)
	bound := terminalStateRaw(t, binding, binding.Backend.Serial, false)
	digest := sha256.Sum256(bound)
	binding.Backend.RawSHA256 = hex.EncodeToString(digest[:])
	post := terminalStateRaw(t, binding, binding.Backend.Serial+1, true)
	return binding, &terminalBackendFixture{bound: bound, post: post, boundVersion: binding.Backend.VersionID, postVersion: "version-2", latest: []string{"version-2", "version-2"}, versioning: true}
}

func TestObserveTerminalBackendProvesExactPoststate(t *testing.T) {
	binding, reader := terminalBackendSetup(t)
	got, err := ObserveTerminalBackend(context.Background(), reader, binding, "work-project")
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend.VersionID != "version-2" || got.Backend.Serial != binding.Backend.Serial+1 || len(got.RawEvidence) == 0 || len(got.EvidenceSHA256) != 64 {
		t.Fatalf("bad terminal backend observation: %+v", got)
	}
}

func TestObserveTerminalBackendRejectsUnsafePoststate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(Binding, *terminalBackendFixture)
	}{
		{"versioning disabled", func(_ Binding, r *terminalBackendFixture) { r.versioning = false }},
		{"foreign lock", func(_ Binding, r *terminalBackendFixture) { r.locked = true }},
		{"same version", func(b Binding, r *terminalBackendFixture) {
			r.latest = []string{b.Backend.VersionID, b.Backend.VersionID}
		}},
		{"latest changed", func(_ Binding, r *terminalBackendFixture) { r.latest[1] = "version-3" }},
		{"lineage changed", func(b Binding, r *terminalBackendFixture) {
			var state map[string]any
			_ = json.Unmarshal(r.post, &state)
			state["lineage"] = "foreign"
			r.post, _ = json.Marshal(state)
			_ = b
		}},
		{"serial not newer", func(b Binding, r *terminalBackendFixture) { r.post = terminalStateRaw(t, b, b.Backend.Serial, true) }},
		{"serial jump", func(b Binding, r *terminalBackendFixture) { r.post = terminalStateRaw(t, b, b.Backend.Serial+2, true) }},
		{"unsupported state version", func(_ Binding, r *terminalBackendFixture) {
			var state map[string]any
			_ = json.Unmarshal(r.post, &state)
			state["version"] = 3
			r.post, _ = json.Marshal(state)
		}},
		{"target retained", func(b Binding, r *terminalBackendFixture) { r.post = terminalStateRaw(t, b, b.Backend.Serial+1, false) }},
		{"target project membership retained", func(b Binding, r *terminalBackendFixture) {
			var state map[string]any
			_ = json.Unmarshal(r.post, &state)
			resources := state["resources"].([]any)
			project := resources[0].(map[string]any)["instances"].([]any)[0].(map[string]any)["attributes"].(map[string]any)
			project["resources"] = append(project["resources"].([]any), "do:droplet:"+b.DropletID)
			r.post, _ = json.Marshal(state)
		}},
		{"survivor identity changed", func(_ Binding, r *terminalBackendFixture) {
			var state map[string]any
			_ = json.Unmarshal(r.post, &state)
			resources := state["resources"].([]any)
			instances := resources[1].(map[string]any)["instances"].([]any)
			instances[0].(map[string]any)["attributes"].(map[string]any)["id"] = "999"
			r.post, _ = json.Marshal(state)
		}},
		{"survivor project changed", func(_ Binding, r *terminalBackendFixture) {
			var state map[string]any
			_ = json.Unmarshal(r.post, &state)
			resources := state["resources"].([]any)
			project := resources[0].(map[string]any)["instances"].([]any)[0].(map[string]any)["attributes"].(map[string]any)
			project["project"] = "foreign-project"
			r.post, _ = json.Marshal(state)
		}},
		{"survivor vpc changed", func(_ Binding, r *terminalBackendFixture) {
			var state map[string]any
			_ = json.Unmarshal(r.post, &state)
			resources := state["resources"].([]any)
			instances := resources[1].(map[string]any)["instances"].([]any)
			instances[0].(map[string]any)["attributes"].(map[string]any)["vpc_uuid"] = "foreign-vpc"
			r.post, _ = json.Marshal(state)
		}},
		{"survivor size changed", func(_ Binding, r *terminalBackendFixture) {
			var state map[string]any
			_ = json.Unmarshal(r.post, &state)
			resources := state["resources"].([]any)
			instances := resources[1].(map[string]any)["instances"].([]any)
			instances[0].(map[string]any)["attributes"].(map[string]any)["size"] = "s-4vcpu-8gb"
			r.post, _ = json.Marshal(state)
		}},
		{"survivor tags changed", func(_ Binding, r *terminalBackendFixture) {
			var state map[string]any
			_ = json.Unmarshal(r.post, &state)
			resources := state["resources"].([]any)
			instances := resources[1].(map[string]any)["instances"].([]any)
			instances[0].(map[string]any)["attributes"].(map[string]any)["tags"] = []any{"changed"}
			r.post, _ = json.Marshal(state)
		}},
		{"non-droplet attribute changed", func(_ Binding, r *terminalBackendFixture) {
			var state map[string]any
			_ = json.Unmarshal(r.post, &state)
			resources := state["resources"].([]any)
			project := resources[0].(map[string]any)["instances"].([]any)[0].(map[string]any)["attributes"].(map[string]any)
			project["id"] = "other-membership"
			r.post, _ = json.Marshal(state)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding, reader := terminalBackendSetup(t)
			tc.mutate(binding, reader)
			if _, err := ObserveTerminalBackend(context.Background(), reader, binding, "work-project"); !errors.Is(err, ErrTerminalProof) {
				t.Fatalf("unsafe poststate accepted: %v", err)
			}
		})
	}
}

func TestObserveTerminalBackendRejectsSplitBoundProjectMembership(t *testing.T) {
	binding, reader := terminalBackendSetup(t)
	for _, raw := range []*[]byte{&reader.bound, &reader.post} {
		var state map[string]any
		if err := json.Unmarshal(*raw, &state); err != nil {
			t.Fatal(err)
		}
		resources := state["resources"].([]any)
		project := resources[0].(map[string]any)
		attributes := project["instances"].([]any)[0].(map[string]any)["attributes"].(map[string]any)
		members := attributes["resources"].([]any)
		attributes["resources"] = append([]any(nil), members[:2]...)
		second := map[string]any{
			"mode":     "managed",
			"type":     "digitalocean_project_resources",
			"name":     "split",
			"provider": project["provider"],
			"instances": []any{map[string]any{"attributes": map[string]any{
				"id":        "split-membership-id",
				"project":   "work-project",
				"resources": append([]any(nil), members[2:]...),
			}}},
		}
		state["resources"] = append(resources, second)
		var err error
		*raw, err = json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
	}
	digest := sha256.Sum256(reader.bound)
	binding.Backend.RawSHA256 = hex.EncodeToString(digest[:])
	if _, err := ObserveTerminalBackend(context.Background(), reader, binding, "work-project"); !errors.Is(err, ErrTerminalProof) {
		t.Fatalf("split bound project membership accepted: %v", err)
	}
}

func TestObserveTerminalBackendRejectsUntrustedBoundProjectIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing project", func(project map[string]any) { delete(project, "project") }},
		{"wrong project", func(project map[string]any) { project["project"] = "foreign-project" }},
		{"wrong provider", func(project map[string]any) { project["provider"] = `provider["registry.opentofu.org/hashicorp/aws"]` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding, reader := terminalBackendSetup(t)
			for _, raw := range []*[]byte{&reader.bound, &reader.post} {
				var state map[string]any
				if err := json.Unmarshal(*raw, &state); err != nil {
					t.Fatal(err)
				}
				resource := state["resources"].([]any)[0].(map[string]any)
				attributes := resource["instances"].([]any)[0].(map[string]any)["attributes"].(map[string]any)
				if tc.name == "wrong provider" {
					tc.mutate(resource)
				} else {
					tc.mutate(attributes)
				}
				var err error
				*raw, err = json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
			}
			digest := sha256.Sum256(reader.bound)
			binding.Backend.RawSHA256 = hex.EncodeToString(digest[:])
			if _, err := ObserveTerminalBackend(context.Background(), reader, binding, "work-project"); !errors.Is(err, ErrTerminalProof) {
				t.Fatalf("untrusted bound project accepted: %v", err)
			}
		})
	}
}

func TestObserveTerminalBackendRejectsUntrustedDropletProvider(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider any
	}{
		{"missing", nil},
		{"foreign", `provider["registry.opentofu.org/hashicorp/aws"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding, reader := terminalBackendSetup(t)
			for _, raw := range []*[]byte{&reader.bound, &reader.post} {
				var state map[string]any
				if err := json.Unmarshal(*raw, &state); err != nil {
					t.Fatal(err)
				}
				droplets := state["resources"].([]any)[1].(map[string]any)
				if tc.provider == nil {
					delete(droplets, "provider")
				} else {
					droplets["provider"] = tc.provider
				}
				var err error
				*raw, err = json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
			}
			digest := sha256.Sum256(reader.bound)
			binding.Backend.RawSHA256 = hex.EncodeToString(digest[:])
			if _, err := ObserveTerminalBackend(context.Background(), reader, binding, "work-project"); !errors.Is(err, ErrTerminalProof) {
				t.Fatalf("untrusted droplet provider accepted: %v", err)
			}
		})
	}
}
