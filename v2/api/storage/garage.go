package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type GarageAdminClient struct {
	endpoint string
	token    string
	http     *http.Client
}

type GarageKey struct {
	AccessKeyID     string `json:"accessKeyId"`
	Name            string `json:"name"`
	SecretAccessKey string `json:"secretAccessKey"`
}

type GarageBucket struct {
	ID            string   `json:"id"`
	GlobalAliases []string `json:"globalAliases"`
}

type BucketKeyPermissions struct {
	Read  bool `json:"read,omitempty"`
	Write bool `json:"write,omitempty"`
	Owner bool `json:"owner,omitempty"`
}

func NewGarageAdminClient(endpoint, token string) *GarageAdminClient {
	return &GarageAdminClient{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (c *GarageAdminClient) EnsureBucket(ctx context.Context, name string) (*GarageBucket, bool, error) {
	bucket, err := c.GetBucket(ctx, name)
	if err == nil {
		return bucket, false, nil
	}
	if !isGarageNotFound(err) {
		return nil, false, err
	}
	bucket, err = c.CreateBucket(ctx, name)
	if err != nil {
		return nil, false, err
	}
	return bucket, true, nil
}

func (c *GarageAdminClient) GetBucket(ctx context.Context, name string) (*GarageBucket, error) {
	var out GarageBucket
	path := "/v2/GetBucketInfo?globalAlias=" + url.QueryEscape(name)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *GarageAdminClient) CreateBucket(ctx context.Context, name string) (*GarageBucket, error) {
	req := map[string]interface{}{
		"globalAlias": name,
	}
	var out GarageBucket
	if err := c.do(ctx, http.MethodPost, "/v2/CreateBucket", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *GarageAdminClient) EnsureKey(ctx context.Context, name string) (*GarageKey, bool, error) {
	key, err := c.GetKey(ctx, name, true)
	if err == nil && key.SecretAccessKey != "" {
		return key, false, nil
	}
	if err != nil && !isGarageNotFound(err) {
		return nil, false, err
	}
	key, err = c.CreateKey(ctx, name)
	if err != nil {
		return nil, false, err
	}
	return key, true, nil
}

func (c *GarageAdminClient) GetKey(ctx context.Context, name string, showSecret bool) (*GarageKey, error) {
	var out GarageKey
	path := "/v2/GetKeyInfo?search=" + url.QueryEscape(name)
	if showSecret {
		path += "&showSecretKey=true"
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	if out.Name != name {
		return nil, garageStatusError{status: http.StatusNotFound, body: "key not found"}
	}
	return &out, nil
}

func (c *GarageAdminClient) CreateKey(ctx context.Context, name string) (*GarageKey, error) {
	req := map[string]interface{}{
		"name":         name,
		"neverExpires": true,
	}
	var out GarageKey
	if err := c.do(ctx, http.MethodPost, "/v2/CreateKey", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *GarageAdminClient) AllowBucketKey(ctx context.Context, bucketID, accessKeyID string, permissions BucketKeyPermissions) error {
	req := map[string]interface{}{
		"bucketId":    bucketID,
		"accessKeyId": accessKeyID,
		"permissions": permissions,
	}
	return c.do(ctx, http.MethodPost, "/v2/AllowBucketKey", req, nil)
}

func (c *GarageAdminClient) do(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	var reader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		return garageStatusError{status: resp.StatusCode, body: buf.String()}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type garageStatusError struct {
	status int
	body   string
}

func (e garageStatusError) Error() string {
	return fmt.Sprintf("garage admin status %d: %s", e.status, strings.TrimSpace(e.body))
}

func isGarageNotFound(err error) bool {
	if e, ok := err.(garageStatusError); ok {
		return e.status == http.StatusNotFound || e.status == http.StatusBadRequest || e.status == http.StatusInternalServerError && strings.Contains(strings.ToLower(e.body), "not found")
	}
	return false
}
