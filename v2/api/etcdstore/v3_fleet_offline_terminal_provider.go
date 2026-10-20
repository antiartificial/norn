package etcdstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"norn/v2/api/fleet/offline"
)

const offlineDONotFoundMessage = "The resource you requested could not be found."

type offlineProviderAbsenceObservation struct {
	ObservedAt     time.Time
	EvidenceSHA256 string
	RawEvidence    []byte
}

// offlineDOAbsenceSource is authority-owned. Its credential supplier is never
// reachable from HTTP, and its result is only one input to terminal evidence.
type offlineDOAbsenceSource struct {
	reader   offlineDOReader
	topology offlineProviderTopology
	clock    func() time.Time
}

func newOfflineDOAbsenceSource(token func(context.Context) (string, error), projectID, vpcUUID string, requiredTags []string) offlineDOAbsenceSource {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return offlineDOAbsenceSource{
		reader: offlineDOReader{client: client, token: token, projectID: projectID},
		topology: offlineProviderTopology{AccountUUID: offlineWorkAccountUUID, ProjectID: projectID, VPCUUID: vpcUUID,
			RequiredTags: append([]string(nil), requiredTags...)},
		clock: time.Now,
	}
}

func (s offlineDOAbsenceSource) ObserveExactWorkNodeAbsent(ctx context.Context, binding offline.Binding) (offlineProviderAbsenceObservation, error) {
	var zero offlineProviderAbsenceObservation
	if s.reader.client == nil || s.reader.token == nil || s.clock == nil || binding.Validate() != nil ||
		binding.ProviderAccountUUID != offlineWorkAccountUUID || binding.NodeName != offline.WorkNode || binding.DropletID != offline.WorkDropletID ||
		s.topology.AccountUUID != offlineWorkAccountUUID || s.topology.ProjectID == "" || s.topology.VPCUUID == "" || len(s.topology.RequiredTags) == 0 {
		return zero, offline.ErrTerminalProof
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	token, err := s.reader.token(ctx)
	if err != nil || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return zero, offline.ErrTerminalProof
	}
	accountBefore, before, err := s.observeInventoryWithToken(ctx, token)
	if err != nil || accountBefore != offlineWorkAccountUUID || !exactAbsentInventory(before, binding) {
		return zero, offline.ErrTerminalProof
	}
	notFound, err := s.readExactNotFound(ctx, token, binding.DropletID)
	if err != nil {
		return zero, err
	}
	accountAfter, after, err := s.observeInventoryWithToken(ctx, token)
	if err != nil || accountAfter != accountBefore || !exactAbsentInventory(after, binding) {
		return zero, offline.ErrTerminalProof
	}
	canonicalizeProviderRows(before)
	canonicalizeProviderRows(after)
	beforeRaw, _ := json.Marshal(before)
	afterRaw, _ := json.Marshal(after)
	if !bytes.Equal(beforeRaw, afterRaw) {
		return zero, offline.ErrTerminalProof
	}
	evidence, err := json.Marshal(struct {
		AccountUUID string                   `json:"accountUUID"`
		ProjectID   string                   `json:"projectID"`
		VPCUUID     string                   `json:"vpcUUID"`
		DropletID   string                   `json:"dropletID"`
		NodeName    string                   `json:"nodeName"`
		NotFound    json.RawMessage          `json:"notFound"`
		Inventory   []offlineProviderDroplet `json:"inventory"`
	}{accountBefore, s.topology.ProjectID, s.topology.VPCUUID, binding.DropletID, binding.NodeName, notFound, before})
	if err != nil {
		return zero, offline.ErrTerminalProof
	}
	digest := sha256.Sum256(evidence)
	return offlineProviderAbsenceObservation{ObservedAt: s.clock().UTC(), EvidenceSHA256: hex.EncodeToString(digest[:]), RawEvidence: evidence}, nil
}

func (s offlineDOAbsenceSource) observeInventoryWithToken(ctx context.Context, token string) (string, []offlineProviderDroplet, error) {
	account, err := s.reader.accountUUID(ctx, token)
	if err != nil {
		return "", nil, err
	}
	rows, err := s.reader.droplets(ctx, token)
	return account, rows, err
}

func (s offlineDOAbsenceSource) readExactNotFound(ctx context.Context, token, dropletID string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.digitalocean.com/v2/droplets/"+dropletID, nil)
	if err != nil {
		return nil, offline.ErrTerminalProof
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	client := *s.reader.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, offline.ErrTerminalProof
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || len(raw) > 16<<10 || response.StatusCode != http.StatusNotFound || mediaErr != nil || mediaType != "application/json" {
		return nil, offline.ErrTerminalProof
	}
	var envelope struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.ID != "not_found" || envelope.Message != offlineDONotFoundMessage {
		return nil, offline.ErrTerminalProof
	}
	canonical, _ := json.Marshal(envelope)
	if !bytes.Equal(canonical, raw) {
		return nil, offline.ErrTerminalProof
	}
	return append([]byte(nil), raw...), nil
}

func exactAbsentInventory(rows []offlineProviderDroplet, binding offline.Binding) bool {
	if len(rows) == 0 {
		return false
	}
	for _, row := range rows {
		if row.ID == binding.DropletID {
			return false
		}
		// A same-name row anywhere in the authenticated account is a possible
		// replacement. Missing or changed project/VPC/tags cannot make it safe.
		if row.Name == binding.NodeName {
			return false
		}
		if row.ID == "" {
			return false
		}
		if _, err := strconv.ParseUint(row.ID, 10, 64); err != nil {
			return false
		}
	}
	return true
}

func canonicalizeProviderRows(rows []offlineProviderDroplet) {
	for i := range rows {
		sort.Strings(rows[i].Tags)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
}
