package etcdstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"norn/v2/api/fleet/offline"
)

const offlineWorkAccountUUID = "17f40050233d1cd4962b44050e82a0dd01f8dff7"

// offlineProviderInventoryReader is an authority-owned, read-only provider
// client. The runner's inventory and prechange receipt must never implement it.
type offlineProviderInventoryReader interface {
	ObserveInventory(context.Context) (string, []offlineProviderDroplet, error)
}

type offlineProviderDroplet struct {
	ID, Name, Status, VPCUUID, ProjectID string
	Tags                                 []string
}

// The expected project, VPC and tags are authority configuration, not values
// learned from the runner. Empty configuration cannot authorize an effect.
type offlineProviderTopology struct {
	AccountUUID, ProjectID, VPCUUID string
	RequiredTags                    []string
}

type providerCheckedEffectReobserver struct {
	remaining offlineEffectReobserver
	provider  offlineProviderInventoryReader
	topology  offlineProviderTopology
}

func (r providerCheckedEffectReobserver) reobserveEffect(ctx context.Context, binding offline.Binding, attempt offline.Attempt) (offline.EffectObservation, error) {
	if r.remaining == nil || r.provider == nil || binding.Validate() != nil ||
		r.topology.AccountUUID == "" || r.topology.ProjectID == "" || r.topology.VPCUUID == "" || len(r.topology.RequiredTags) == 0 ||
		r.topology.AccountUUID != offlineWorkAccountUUID || r.topology.AccountUUID != binding.ProviderAccountUUID {
		return offline.EffectObservation{}, offline.ErrEffectProofMissing
	}
	observation, err := r.remaining.reobserveEffect(ctx, binding, attempt)
	if err != nil {
		return offline.EffectObservation{}, err
	}
	observation.ProviderExact = false
	account, droplets, err := r.provider.ObserveInventory(ctx)
	if err != nil || account != r.topology.AccountUUID {
		return offline.EffectObservation{}, offline.ErrEffectProofMissing
	}
	if !exactOfflineProviderTopology(binding, droplets, r.topology) {
		return offline.EffectObservation{}, offline.ErrEffectProofMissing
	}
	observation.ProviderExact = true
	return observation, nil
}

func exactOfflineProviderTopology(binding offline.Binding, droplets []offlineProviderDroplet, expected offlineProviderTopology) bool {
	if len(binding.ProviderNodes) == 0 || len(droplets) == 0 {
		return false
	}
	wanted := make(map[string]string, len(binding.ProviderNodes))
	for _, node := range binding.ProviderNodes {
		wanted[node.ProviderID] = node.Name
	}
	seen := make(map[string]bool, len(wanted))
	for _, droplet := range droplets {
		// Unrelated account resources are permitted, but a duplicate ID, name or
		// unexpected node in the target project/VPC is ambiguous and fails closed.
		name, named := wanted[droplet.ID]
		inTarget := droplet.ProjectID == expected.ProjectID && droplet.VPCUUID == expected.VPCUUID
		if !named && !inTarget {
			continue
		}
		if !named || seen[droplet.ID] || droplet.ID == "" || droplet.Name != name || droplet.Status != "active" ||
			droplet.ProjectID != expected.ProjectID || droplet.VPCUUID != expected.VPCUUID || !containsAllTags(droplet.Tags, expected.RequiredTags) {
			return false
		}
		if _, err := strconv.ParseUint(droplet.ID, 10, 64); err != nil {
			return false
		}
		seen[droplet.ID] = true
	}
	return len(seen) == len(wanted) && seen[binding.DropletID]
}

func containsAllTags(actual, required []string) bool {
	if len(required) == 0 {
		return false
	}
	set := make(map[string]bool, len(actual))
	for _, tag := range actual {
		if tag == "" || set[tag] {
			return false
		}
		set[tag] = true
	}
	for _, tag := range required {
		if tag == "" || !set[tag] {
			return false
		}
	}
	return true
}

// offlineDOReader uses only GET endpoints. Token is supplied by the authority
// at call time and must have account, droplet and project read scopes.
type offlineDOReader struct {
	client    *http.Client
	token     func(context.Context) (string, error)
	projectID string
}

func (r offlineDOReader) get(ctx context.Context, token, path string, out any) error {
	if r.client == nil || r.token == nil || r.projectID == "" {
		return offline.ErrEffectProofMissing
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return offline.ErrEffectProofMissing
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.digitalocean.com"+path, nil)
	if err != nil {
		return offline.ErrEffectProofMissing
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	client := *r.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return offline.ErrEffectProofMissing
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return offline.ErrEffectProofMissing
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return offline.ErrEffectProofMissing
	}
	if json.Unmarshal(data, out) != nil {
		return offline.ErrEffectProofMissing
	}
	return nil
}

// ObserveInventory pins one read-only bearer across account, project and
// droplet reads. A rotating supplier cannot splice identities across pages.
func (r offlineDOReader) ObserveInventory(ctx context.Context) (string, []offlineProviderDroplet, error) {
	if r.token == nil {
		return "", nil, offline.ErrEffectProofMissing
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, err := r.token(ctx)
	if err != nil || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", nil, offline.ErrEffectProofMissing
	}
	account, err := r.accountUUID(ctx, token)
	if err != nil {
		return "", nil, err
	}
	droplets, err := r.droplets(ctx, token)
	if err != nil {
		return "", nil, err
	}
	return account, droplets, nil
}

func (r offlineDOReader) accountUUID(ctx context.Context, token string) (string, error) {
	var response struct {
		Account struct {
			UUID string `json:"uuid"`
		} `json:"account"`
	}
	if err := r.get(ctx, token, "/v2/account", &response); err != nil || response.Account.UUID == "" {
		return "", offline.ErrEffectProofMissing
	}
	return response.Account.UUID, nil
}

func (r offlineDOReader) droplets(ctx context.Context, token string) ([]offlineProviderDroplet, error) {
	if r.projectID == "" {
		return nil, offline.ErrEffectProofMissing
	}
	members, err := r.projectDropletIDs(ctx, token)
	if err != nil {
		return nil, err
	}
	var droplets []offlineProviderDroplet
	seen := map[string]bool{}
	firstTotal := 0
	for page := 1; page <= 50; page++ {
		var response struct {
			Droplets []struct {
				ID      uint64   `json:"id"`
				Name    string   `json:"name"`
				Status  string   `json:"status"`
				VPCUUID string   `json:"vpc_uuid"`
				Tags    []string `json:"tags"`
			} `json:"droplets"`
			Meta struct {
				Total int `json:"total"`
			} `json:"meta"`
		}
		if err := r.get(ctx, token, fmt.Sprintf("/v2/droplets?per_page=200&page=%d", page), &response); err != nil {
			return nil, err
		}
		if page == 1 {
			firstTotal = response.Meta.Total
		}
		if response.Meta.Total <= 0 || response.Meta.Total != firstTotal || response.Meta.Total > 10000 || len(response.Droplets) == 0 || len(droplets)+len(response.Droplets) > response.Meta.Total {
			return nil, offline.ErrEffectProofMissing
		}
		for _, d := range response.Droplets {
			id := strconv.FormatUint(d.ID, 10)
			if d.ID == 0 || seen[id] {
				return nil, offline.ErrEffectProofMissing
			}
			seen[id] = true
			projectID := ""
			if members[id] {
				projectID = r.projectID
			}
			droplets = append(droplets, offlineProviderDroplet{ID: id, Name: d.Name, Status: d.Status, VPCUUID: d.VPCUUID, ProjectID: projectID, Tags: d.Tags})
		}
		if len(droplets) == response.Meta.Total {
			for id := range members {
				if !seen[id] {
					return nil, offline.ErrEffectProofMissing
				}
			}
			freshMembers, err := r.projectDropletIDs(ctx, token)
			if err != nil || !maps.Equal(members, freshMembers) {
				return nil, offline.ErrEffectProofMissing
			}
			return droplets, nil
		}
	}
	return nil, offline.ErrEffectProofMissing
}

func (r offlineDOReader) projectDropletIDs(ctx context.Context, token string) (map[string]bool, error) {
	members := map[string]bool{}
	count := 0
	firstTotal := 0
	for page := 1; page <= 50; page++ {
		var response struct {
			Resources []struct {
				URN string `json:"urn"`
			} `json:"resources"`
			Meta struct {
				Total int `json:"total"`
			} `json:"meta"`
		}
		path := fmt.Sprintf("/v2/projects/%s/resources?per_page=200&page=%d", url.PathEscape(r.projectID), page)
		if err := r.get(ctx, token, path, &response); err != nil {
			return nil, err
		}
		if page == 1 {
			firstTotal = response.Meta.Total
		}
		if response.Meta.Total <= 0 || response.Meta.Total != firstTotal || response.Meta.Total > 10000 || len(response.Resources) == 0 || count+len(response.Resources) > response.Meta.Total {
			return nil, offline.ErrEffectProofMissing
		}
		for _, resource := range response.Resources {
			if strings.HasPrefix(resource.URN, "do:droplet:") {
				id := strings.TrimPrefix(resource.URN, "do:droplet:")
				if id == "" || members[id] {
					return nil, offline.ErrEffectProofMissing
				}
				members[id] = true
			}
		}
		count += len(response.Resources)
		if count == response.Meta.Total {
			return members, nil
		}
	}
	return nil, offline.ErrEffectProofMissing
}
