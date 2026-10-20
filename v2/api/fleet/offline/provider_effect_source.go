package offline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrProviderEffectSource = errors.New("independent provider effect observation is incomplete")

// DigitalOceanEffectSource reads the work account with its own read-only token.
// It never accepts inventory or a token from the effect runner's receipt.
// The caller must verify its returned transcript together with the other
// effect-time sources before signing an attestation.
type DigitalOceanEffectSource struct {
	Client         *http.Client
	Token          func(context.Context) (string, error)
	ProjectID      string
	VPCUUID        string
	RequiredTags   []string
	App03PrivateIP string
	App03PublicIP  string
}

type providerEffectRead struct {
	Path      string `json:"path"`
	Status    int    `json:"status"`
	RawSHA256 string `json:"rawSHA256"`
	// Fields contains only the parsed identity fields used for this decision.
	// Unknown provider response fields and authorization headers are excluded.
	Fields any `json:"fields"`
}

type providerEffectTranscript struct {
	SchemaVersion string               `json:"schemaVersion"`
	AccountUUID   string               `json:"accountUUID"`
	ProjectID     string               `json:"projectId"`
	VPCUUID       string               `json:"vpcUUID"`
	Reads         []providerEffectRead `json:"reads"`
	Nodes         []providerEffectNode `json:"nodes"`
}

type providerEffectNode struct {
	ID        uint64   `json:"id"`
	Name      string   `json:"name"`
	Status    string   `json:"status"`
	VPCUUID   string   `json:"vpc_uuid"`
	Tags      []string `json:"tags"`
	PrivateIP string   `json:"privateIP"`
	PublicIP  string   `json:"publicIP"`
}

// Observe returns a bounded, credential-redacted JSON transcript of independent
// GETs. Exactly the eight binding nodes must be project members and active in
// the expected VPC; app-03 must also have the separately pinned IP pair.
func (s DigitalOceanEffectSource) Observe(ctx context.Context, binding Binding) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || binding.Validate() != nil || binding.SchemaVersion != BindingSchemaV2 ||
		s.Client == nil || s.Token == nil || uuid.Validate(s.ProjectID) != nil || s.VPCUUID != WorkVPCUUID ||
		len(s.RequiredTags) == 0 || s.App03PrivateIP == "" || s.App03PublicIP == "" ||
		binding.ProviderAccountUUID != "17f40050233d1cd4962b44050e82a0dd01f8dff7" {
		return nil, ErrProviderEffectSource
	}
	privateIP, e1 := netip.ParseAddr(s.App03PrivateIP)
	publicIP, e2 := netip.ParseAddr(s.App03PublicIP)
	if e1 != nil || e2 != nil || !privateIP.IsPrivate() || !publicIP.IsGlobalUnicast() || publicIP.IsPrivate() {
		return nil, ErrProviderEffectSource
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, err := s.Token(ctx)
	if err != nil || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, ErrProviderEffectSource
	}
	transcript := providerEffectTranscript{SchemaVersion: "norn.fleet-provider-effect-transcript/v1", ProjectID: s.ProjectID, VPCUUID: s.VPCUUID}
	var account struct {
		Account struct {
			UUID string `json:"uuid"`
		} `json:"account"`
	}
	if err = s.read(ctx, token, "/v2/account", &account, &transcript); err != nil || account.Account.UUID != binding.ProviderAccountUUID {
		return nil, ErrProviderEffectSource
	}
	transcript.AccountUUID = account.Account.UUID
	transcript.Reads[len(transcript.Reads)-1].Fields = account
	wanted := make(map[string]string, len(binding.ProviderNodes))
	for _, n := range binding.ProviderNodes {
		wanted[n.ProviderID] = n.Name
	}
	members, err := s.projectMembers(ctx, token, wanted, &transcript)
	if err != nil {
		return nil, ErrProviderEffectSource
	}
	for _, n := range binding.ProviderNodes {
		var response struct {
			Droplet struct {
				ID       uint64   `json:"id"`
				Name     string   `json:"name"`
				Status   string   `json:"status"`
				VPCUUID  string   `json:"vpc_uuid"`
				Tags     []string `json:"tags"`
				Networks struct {
					V4 []struct {
						IPAddress string `json:"ip_address"`
						Type      string `json:"type"`
					} `json:"v4"`
				} `json:"networks"`
			} `json:"droplet"`
		}
		if err = s.read(ctx, token, "/v2/droplets/"+n.ProviderID, &response, &transcript); err != nil {
			return nil, ErrProviderEffectSource
		}
		d := response.Droplet
		if strconv.FormatUint(d.ID, 10) != n.ProviderID || d.Name != n.Name || d.Status != "active" || d.VPCUUID != s.VPCUUID || !providerEffectTags(d.Tags, s.RequiredTags) {
			return nil, ErrProviderEffectSource
		}
		row := providerEffectNode{ID: d.ID, Name: d.Name, Status: d.Status, VPCUUID: d.VPCUUID, Tags: append([]string(nil), d.Tags...)}
		sort.Strings(row.Tags)
		for _, network := range d.Networks.V4 {
			switch network.Type {
			case "private":
				if row.PrivateIP != "" {
					return nil, ErrProviderEffectSource
				}
				row.PrivateIP = network.IPAddress
			case "public":
				if row.PublicIP != "" {
					return nil, ErrProviderEffectSource
				}
				row.PublicIP = network.IPAddress
			}
		}
		if n.Name == WorkNode && (row.PrivateIP != s.App03PrivateIP || row.PublicIP != s.App03PublicIP) {
			return nil, ErrProviderEffectSource
		}
		privateAddress, err := netip.ParseAddr(row.PrivateIP)
		if err != nil || !privateAddress.IsPrivate() {
			return nil, ErrProviderEffectSource
		}
		publicAddress, err := netip.ParseAddr(row.PublicIP)
		if err != nil || !publicAddress.IsGlobalUnicast() || publicAddress.IsPrivate() {
			return nil, ErrProviderEffectSource
		}
		transcript.Nodes = append(transcript.Nodes, row)
		transcript.Reads[len(transcript.Reads)-1].Fields = map[string]any{"droplet": row}
	}
	// Project membership is a separate API read. Re-read after all eight
	// droplets so a move during the scan cannot be accepted as a stable result.
	finalMembers, err := s.projectMembers(ctx, token, wanted, &transcript)
	if err != nil || !maps.Equal(members, finalMembers) {
		return nil, ErrProviderEffectSource
	}
	data, err := json.Marshal(transcript)
	if err != nil || len(data) > 128<<10 {
		return nil, ErrProviderEffectSource
	}
	return data, nil
}

func (s DigitalOceanEffectSource) projectMembers(ctx context.Context, token string, wanted map[string]string, transcript *providerEffectTranscript) (map[string]bool, error) {
	members := make(map[string]bool, len(wanted))
	resourceTotal := -1
	resourceCount := 0
	for page := 1; page <= 4; page++ {
		path := fmt.Sprintf("/v2/projects/%s/resources?per_page=200&page=%d", url.PathEscape(s.ProjectID), page)
		var response struct {
			Resources []struct {
				URN string `json:"urn"`
			} `json:"resources"`
			Meta struct {
				Total int `json:"total"`
			} `json:"meta"`
		}
		if err := s.read(ctx, token, path, &response, transcript); err != nil || response.Meta.Total < 8 || response.Meta.Total > 800 || len(response.Resources) == 0 {
			return nil, ErrProviderEffectSource
		}
		if resourceTotal == -1 {
			resourceTotal = response.Meta.Total
		}
		resourceCount += len(response.Resources)
		if response.Meta.Total != resourceTotal || resourceCount > resourceTotal {
			return nil, ErrProviderEffectSource
		}
		transcript.Reads[len(transcript.Reads)-1].Fields = response
		for _, resource := range response.Resources {
			if !strings.HasPrefix(resource.URN, "do:droplet:") {
				continue
			}
			id := strings.TrimPrefix(resource.URN, "do:droplet:")
			if wanted[id] == "" || members[id] {
				return nil, ErrProviderEffectSource
			}
			members[id] = true
		}
		if resourceCount == response.Meta.Total {
			break
		}
		if page == 4 {
			return nil, ErrProviderEffectSource
		}
	}
	if len(members) != len(wanted) {
		return nil, ErrProviderEffectSource
	}
	return members, nil
}

func providerEffectTags(actual, required []string) bool {
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

func (s DigitalOceanEffectSource) read(ctx context.Context, token, path string, dst any, transcript *providerEffectTranscript) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.digitalocean.com"+path, nil)
	if err != nil {
		return ErrProviderEffectSource
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	client := *s.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return ErrProviderEffectSource
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrProviderEffectSource
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 || json.Unmarshal(body, dst) != nil {
		return ErrProviderEffectSource
	}
	digest := sha256.Sum256(body)
	transcript.Reads = append(transcript.Reads, providerEffectRead{Path: path, Status: response.StatusCode, RawSHA256: hex.EncodeToString(digest[:])})
	return nil
}
