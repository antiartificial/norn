package offline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

type TerminalBackendReader interface {
	LatestBackendReader
	VersionSize(context.Context, string, string, string) (int64, error)
	ForeignLockAbsent(context.Context, string, string) error
}

type TerminalBackendObservation struct {
	Backend        Backend
	EvidenceSHA256 string
	RawEvidence    []byte
}

type terminalState struct {
	Version          int    `json:"version"`
	TerraformVersion string `json:"terraform_version"`
	Lineage          string `json:"lineage"`
	Serial           uint64 `json:"serial"`
}

// ObserveTerminalBackend proves a stable, newer versioned state after the
// deletion. It retains the exact bytes privately; it grants no terminal state
// until provider and workload observations are also composed.
func ObserveTerminalBackend(ctx context.Context, reader TerminalBackendReader, binding Binding, projectID string) (TerminalBackendObservation, error) {
	var zero TerminalBackendObservation
	if reader == nil || binding.Validate() != nil || projectID == "" {
		return zero, ErrTerminalProof
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if enabled, err := reader.VersioningEnabled(ctx, binding.Backend.Bucket, binding.Backend.Key); err != nil || !enabled {
		return zero, ErrTerminalProof
	}
	if err := reader.ForeignLockAbsent(ctx, binding.Backend.Bucket, binding.Backend.Key); err != nil {
		return zero, ErrTerminalProof
	}
	boundSize, err := reader.VersionSize(ctx, binding.Backend.Bucket, binding.Backend.Key, binding.Backend.VersionID)
	if err != nil {
		return zero, ErrTerminalProof
	}
	boundRaw, err := reader.Version(ctx, binding.Backend.Bucket, binding.Backend.Key, binding.Backend.VersionID, boundSize)
	if err != nil || int64(len(boundRaw)) != boundSize {
		return zero, ErrTerminalProof
	}
	boundDigest := sha256.Sum256(boundRaw)
	if hex.EncodeToString(boundDigest[:]) != binding.Backend.RawSHA256 {
		return zero, ErrTerminalProof
	}
	version, size, err := reader.Latest(ctx, binding.Backend.Bucket, binding.Backend.Key)
	if err != nil || version == "" || version == "null" || version == binding.Backend.VersionID || size <= 0 || size > maxEffectStateBytes {
		return zero, ErrTerminalProof
	}
	postRaw, err := reader.Version(ctx, binding.Backend.Bucket, binding.Backend.Key, version, size)
	if err != nil || int64(len(postRaw)) != size {
		return zero, ErrTerminalProof
	}
	boundState, err := decodeTerminalState(boundRaw)
	if err != nil || boundState.Lineage != binding.Backend.Lineage || boundState.Serial != binding.Backend.Serial {
		return zero, ErrTerminalProof
	}
	postState, err := decodeTerminalState(postRaw)
	if err != nil || postState.Lineage != boundState.Lineage || postState.Serial != boundState.Serial+1 ||
		!exactTerminalStateDelta(boundRaw, postRaw, binding, projectID) {
		return zero, ErrTerminalProof
	}
	latest, latestSize, err := reader.Latest(ctx, binding.Backend.Bucket, binding.Backend.Key)
	if err != nil || latest != version || latestSize != size {
		return zero, ErrTerminalProof
	}
	if enabled, err := reader.VersioningEnabled(ctx, binding.Backend.Bucket, binding.Backend.Key); err != nil || !enabled || reader.ForeignLockAbsent(ctx, binding.Backend.Bucket, binding.Backend.Key) != nil {
		return zero, ErrTerminalProof
	}
	postDigest := sha256.Sum256(postRaw)
	backend := Backend{Bucket: binding.Backend.Bucket, Key: binding.Backend.Key, VersionID: version, Lineage: postState.Lineage, Serial: postState.Serial, RawSHA256: hex.EncodeToString(postDigest[:])}
	evidence, _ := json.Marshal(struct {
		Bound Backend         `json:"bound"`
		Post  Backend         `json:"post"`
		Raw   json.RawMessage `json:"rawState"`
	}{binding.Backend, backend, postRaw})
	evidenceDigest := sha256.Sum256(evidence)
	return TerminalBackendObservation{Backend: backend, EvidenceSHA256: hex.EncodeToString(evidenceDigest[:]), RawEvidence: evidence}, nil
}

func decodeTerminalState(raw []byte) (terminalState, error) {
	var state terminalState
	if len(raw) == 0 || len(raw) > maxEffectStateBytes || !json.Valid(raw) || rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(raw))) != nil {
		return state, ErrTerminalProof
	}
	// Terraform state has additional top-level fields. Decode a narrow envelope
	// after validating the entire document for duplicate keys.
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || json.Unmarshal(envelope["lineage"], &state.Lineage) != nil ||
		json.Unmarshal(envelope["serial"], &state.Serial) != nil || json.Unmarshal(envelope["version"], &state.Version) != nil ||
		json.Unmarshal(envelope["terraform_version"], &state.TerraformVersion) != nil || state.Version != 4 || state.TerraformVersion != "1.10.6" || state.Lineage == "" || state.Serial == 0 || len(envelope["resources"]) == 0 {
		return terminalState{}, ErrTerminalProof
	}
	return state, nil
}

func exactTerminalStateDelta(beforeRaw, afterRaw []byte, binding Binding, projectID string) bool {
	decode := func(raw []byte) (map[string]any, bool) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var state map[string]any
		return state, decoder.Decode(&state) == nil
	}
	before, ok := decode(beforeRaw)
	if !ok {
		return false
	}
	if !exactBoundProjectMembership(before, binding, projectID) {
		return false
	}
	after, ok := decode(afterRaw)
	if !ok {
		return false
	}
	before["serial"] = after["serial"]
	resources, ok := before["resources"].([]any)
	if !ok {
		return false
	}
	removedDroplet, removedMembership := 0, 0
	for _, item := range resources {
		resource, ok := item.(map[string]any)
		if !ok || resource["mode"] != "managed" {
			continue
		}
		instances, ok := resource["instances"].([]any)
		if !ok {
			return false
		}
		switch resource["type"] {
		case "digitalocean_droplet":
			if resource["provider"] != `provider["registry.opentofu.org/digitalocean/digitalocean"]` {
				return false
			}
			kept := instances[:0]
			for _, rawInstance := range instances {
				instance, _ := rawInstance.(map[string]any)
				attributes, _ := instance["attributes"].(map[string]any)
				if attributes["id"] == binding.DropletID && attributes["name"] == binding.NodeName {
					removedDroplet++
					continue
				}
				kept = append(kept, rawInstance)
			}
			resource["instances"] = kept
		case "digitalocean_project", "digitalocean_project_resources":
			for _, rawInstance := range instances {
				instance, _ := rawInstance.(map[string]any)
				attributes, _ := instance["attributes"].(map[string]any)
				members, ok := attributes["resources"].([]any)
				if !ok {
					continue
				}
				kept := members[:0]
				for _, member := range members {
					if member == "do:droplet:"+binding.DropletID {
						removedMembership++
						continue
					}
					kept = append(kept, member)
				}
				attributes["resources"] = kept
			}
		}
	}
	if removedDroplet != 1 || removedMembership != 1 {
		return false
	}
	normalizedBefore, errBefore := json.Marshal(before)
	normalizedAfter, errAfter := json.Marshal(after)
	return errBefore == nil && errAfter == nil && bytes.Equal(normalizedBefore, normalizedAfter)
}

func exactBoundProjectMembership(state map[string]any, binding Binding, projectID string) bool {
	resources, ok := state["resources"].([]any)
	if !ok {
		return false
	}
	wanted := map[string]bool{}
	for _, node := range binding.ProviderNodes {
		wanted["do:droplet:"+node.ProviderID] = true
	}
	complete := 0
	for _, item := range resources {
		resource, _ := item.(map[string]any)
		if resource["mode"] != "managed" || (resource["type"] != "digitalocean_project" && resource["type"] != "digitalocean_project_resources") {
			continue
		}
		if resource["provider"] != `provider["registry.opentofu.org/digitalocean/digitalocean"]` {
			return false
		}
		instances, _ := resource["instances"].([]any)
		for _, rawInstance := range instances {
			instance, _ := rawInstance.(map[string]any)
			attributes, _ := instance["attributes"].(map[string]any)
			identityKey := "id"
			if resource["type"] == "digitalocean_project_resources" {
				identityKey = "project"
			}
			if attributes[identityKey] != projectID {
				return false
			}
			members, _ := attributes["resources"].([]any)
			seenBound, allBound := 0, true
			seen := map[string]bool{}
			for _, rawMember := range members {
				member, ok := rawMember.(string)
				if !ok || member == "" || seen[member] {
					return false
				}
				seen[member] = true
				if wanted[member] {
					seenBound++
				}
			}
			for urn := range wanted {
				if !seen[urn] {
					allBound = false
				}
			}
			if seenBound > 0 && !allBound {
				return false
			}
			if allBound {
				complete++
			}
		}
	}
	return complete == 1
}
