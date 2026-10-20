package offline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrProviderEffectSource = errors.New("independent provider effect observation is incomplete")

const (
	WorkProviderAccountUUID = "17f40050233d1cd4962b44050e82a0dd01f8dff7"
	WorkProjectUUID         = "72afc448-43fd-4660-90ed-be72b94ec07c"
	workApp03PrivateIP      = "10.43.0.13"
	workApp03PublicIP       = "159.203.100.16"
	workFleetTag            = "norn:tk-staging-nyc3"
)

// DigitalOceanEffectSource reads the work account with its own read-only token.
// It never accepts inventory or a token from the effect runner's receipt.
// The caller must verify its returned transcript together with the other
// effect-time sources before signing an attestation.
type DigitalOceanEffectSource struct {
	tokenFile string
	roots     *x509.CertPool
	// This override is confined to same-package deterministic tests. Production
	// always uses direct TLS with the exact DigitalOcean server name and roots.
	transport http.RoundTripper
}

// NewDigitalOceanEffectSource accepts only the authority's local owner-only
// read credential and its dedicated provider CA bundle. Topology is pinned in
// this source and cannot be weakened with caller-supplied project or tags.
func NewDigitalOceanEffectSource(tokenFile string, roots *x509.CertPool) (DigitalOceanEffectSource, error) {
	if tokenFile == "" || roots == nil {
		return DigitalOceanEffectSource{}, ErrProviderEffectSource
	}
	return DigitalOceanEffectSource{tokenFile: tokenFile, roots: roots.Clone()}, nil
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
		s.tokenFile == "" || s.roots == nil || binding.ProviderAccountUUID != WorkProviderAccountUUID {
		return nil, ErrProviderEffectSource
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, err := s.loadToken()
	if err != nil || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, ErrProviderEffectSource
	}
	transcript := providerEffectTranscript{SchemaVersion: "norn.fleet-provider-effect-transcript/v1", ProjectID: WorkProjectUUID, VPCUUID: WorkVPCUUID}
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
		row, err := s.readNode(ctx, token, n, &transcript)
		if err != nil {
			return nil, ErrProviderEffectSource
		}
		transcript.Nodes = append(transcript.Nodes, row)
	}
	// A second pass detects identity, lifecycle, network, or tag changes while
	// the first eight individual reads were in flight, even if membership stayed.
	firstNodes := append([]providerEffectNode(nil), transcript.Nodes...)
	for i, n := range binding.ProviderNodes {
		row, err := s.readNode(ctx, token, n, &transcript)
		if err != nil || !reflect.DeepEqual(row, firstNodes[i]) {
			return nil, ErrProviderEffectSource
		}
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
		path := fmt.Sprintf("/v2/projects/%s/resources?per_page=200&page=%d", url.PathEscape(WorkProjectUUID), page)
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

func providerNodePool(name string) string {
	if strings.HasPrefix(name, "app-") {
		return "app"
	}
	if strings.HasPrefix(name, "control-") {
		return "control"
	}
	if strings.HasPrefix(name, "ingress-") {
		return "ingress"
	}
	return ""
}

func (s DigitalOceanEffectSource) readNode(ctx context.Context, token string, n ProviderNode, transcript *providerEffectTranscript) (providerEffectNode, error) {
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
	if s.read(ctx, token, "/v2/droplets/"+n.ProviderID, &response, transcript) != nil {
		return providerEffectNode{}, ErrProviderEffectSource
	}
	d := response.Droplet
	if strconv.FormatUint(d.ID, 10) != n.ProviderID || d.Name != "tk-staging-nyc3-"+n.Name || d.Status != "active" || d.VPCUUID != WorkVPCUUID || !providerEffectTags(d.Tags, []string{workFleetTag, workFleetTag + ":pool:" + providerNodePool(n.Name)}) {
		return providerEffectNode{}, ErrProviderEffectSource
	}
	row := providerEffectNode{ID: d.ID, Name: d.Name, Status: d.Status, VPCUUID: d.VPCUUID, Tags: append([]string(nil), d.Tags...)}
	sort.Strings(row.Tags)
	for _, network := range d.Networks.V4 {
		switch network.Type {
		case "private":
			if row.PrivateIP != "" {
				return providerEffectNode{}, ErrProviderEffectSource
			}
			row.PrivateIP = network.IPAddress
		case "public":
			if row.PublicIP != "" {
				return providerEffectNode{}, ErrProviderEffectSource
			}
			row.PublicIP = network.IPAddress
		}
	}
	if n.Name == WorkNode && (row.PrivateIP != workApp03PrivateIP || row.PublicIP != workApp03PublicIP) {
		return providerEffectNode{}, ErrProviderEffectSource
	}
	privateAddress, e1 := netip.ParseAddr(row.PrivateIP)
	publicAddress, e2 := netip.ParseAddr(row.PublicIP)
	if e1 != nil || e2 != nil || !privateAddress.IsPrivate() || !publicAddress.IsGlobalUnicast() || publicAddress.IsPrivate() {
		return providerEffectNode{}, ErrProviderEffectSource
	}
	transcript.Reads[len(transcript.Reads)-1].Fields = map[string]any{"droplet": row}
	return row, nil
}

func (s DigitalOceanEffectSource) loadToken() (string, error) {
	before, err := os.Lstat(s.tokenFile)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 {
		return "", ErrProviderEffectSource
	}
	f, err := os.Open(s.tokenFile)
	if err != nil {
		return "", ErrProviderEffectSource
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() > 4096 || after.Size() == 0 {
		return "", ErrProviderEffectSource
	}
	stat := reflect.ValueOf(after.Sys())
	if stat.Kind() != reflect.Pointer || stat.IsNil() || stat.Elem().Kind() != reflect.Struct {
		return "", ErrProviderEffectSource
	}
	uid := stat.Elem().FieldByName("Uid")
	if !uid.IsValid() || uid.Kind() != reflect.Uint32 || uid.Uint() != uint64(os.Geteuid()) {
		return "", ErrProviderEffectSource
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return "", ErrProviderEffectSource
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", ErrProviderEffectSource
	}
	return token, nil
}

func providerEffectUniqueJSON(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if !providerEffectUniqueValue(decoder, 0) {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}

func providerEffectUniqueValue(decoder *json.Decoder, depth int) bool {
	if depth > 64 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] {
				return false
			}
			seen[key] = true
			if !providerEffectUniqueValue(decoder, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !providerEffectUniqueValue(decoder, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}

func (s DigitalOceanEffectSource) read(ctx context.Context, token, path string, dst any, transcript *providerEffectTranscript) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.digitalocean.com"+path, nil)
	if err != nil {
		return ErrProviderEffectSource
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	transport := s.transport
	if transport == nil {
		transport = &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: s.roots, ServerName: "api.digitalocean.com"}}
	}
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return ErrProviderEffectSource
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrProviderEffectSource
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 || !providerEffectUniqueJSON(body) || json.Unmarshal(body, dst) != nil {
		return ErrProviderEffectSource
	}
	digest := sha256.Sum256(body)
	transcript.Reads = append(transcript.Reads, providerEffectRead{Path: path, Status: response.StatusCode, RawSHA256: hex.EncodeToString(digest[:])})
	return nil
}
