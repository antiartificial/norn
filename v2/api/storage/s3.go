package storage

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"norn/v2/api/model"
)

type Config struct {
	Endpoint            string
	AccessKey           string
	SecretKey           string
	Region              string
	UseSSL              bool
	Provider            string
	ForcePathStyle      bool
	GarageAdminEndpoint string
	GarageAdminToken    string
}

type Client struct {
	mc     *minio.Client
	config Config
	garage *GarageAdminClient
}

func NewClient(cfg Config) (*Client, error) {
	endpoint, useSSL := normalizeEndpoint(cfg.Endpoint, cfg.UseSSL)
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: useSSL,
		Region: cfg.Region,
	}
	if cfg.ForcePathStyle {
		opts.BucketLookup = minio.BucketLookupPath
	}
	mc, err := minio.New(endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}
	cfg.Endpoint = endpoint
	cfg.UseSSL = useSSL
	var garage *GarageAdminClient
	if strings.EqualFold(cfg.Provider, "garage") && cfg.GarageAdminEndpoint != "" && cfg.GarageAdminToken != "" {
		garage = NewGarageAdminClient(cfg.GarageAdminEndpoint, cfg.GarageAdminToken)
	}
	return &Client{mc: mc, config: cfg, garage: garage}, nil
}

func (c *Client) CreateBucket(ctx context.Context, name string) error {
	exists, err := c.mc.BucketExists(ctx, name)
	if err != nil {
		return fmt.Errorf("check bucket %s: %w", name, err)
	}
	if exists {
		log.Printf("s3: bucket %s already exists", name)
		return nil
	}
	region := c.config.Region
	if region == "" {
		region = "us-east-1"
	}
	if err := c.mc.MakeBucket(ctx, name, minio.MakeBucketOptions{Region: region}); err != nil {
		return fmt.Errorf("create bucket %s: %w", name, err)
	}
	log.Printf("s3: created bucket %s", name)
	return nil
}

func (c *Client) BucketExists(ctx context.Context, name string) (bool, error) {
	return c.mc.BucketExists(ctx, name)
}

func (c *Client) DeleteBucket(ctx context.Context, name string) error {
	return c.mc.RemoveBucket(ctx, name)
}

func (c *Client) Healthy(ctx context.Context) error {
	_, err := c.mc.ListBuckets(ctx)
	return err
}

type ProvisionResult struct {
	Env     map[string]string
	Secrets map[string]string
	Buckets []ProvisionedBucket
}

type ProvisionedBucket struct {
	Name     string
	Access   string
	Provider string
	Mode     string
	Created  bool
}

func (c *Client) ProvisionAppStorage(ctx context.Context, appID string, spec *model.ObjectStorageInfra, _ map[string]string) (*ProvisionResult, error) {
	result := &ProvisionResult{
		Env:     map[string]string{},
		Secrets: map[string]string{},
	}
	if spec == nil {
		return result, nil
	}
	provider := spec.Provider
	if provider == "" {
		provider = "garage"
	}
	if len(spec.Buckets) == 0 {
		return nil, fmt.Errorf("object storage declared without buckets")
	}

	accessKey := c.config.AccessKey
	secretKey := c.config.SecretKey
	mode := "shared"

	if strings.EqualFold(provider, "garage") && c.garage != nil {
		keyName := fmt.Sprintf("norn-%s", appID)
		key, created, err := c.garage.EnsureKey(ctx, keyName)
		if err != nil {
			return nil, fmt.Errorf("garage key %s: %w", keyName, err)
		}
		if key.AccessKeyID != "" && key.SecretAccessKey != "" {
			accessKey = key.AccessKeyID
			secretKey = key.SecretAccessKey
			mode = "managed"
			result.Secrets["AWS_ACCESS_KEY_ID"] = accessKey
			result.Secrets["AWS_SECRET_ACCESS_KEY"] = secretKey
		}
		if created {
			log.Printf("garage: created app key %s", keyName)
		}
	}

	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("object storage credentials are not configured")
	}

	var bucketNames []string
	for i, bucket := range spec.Buckets {
		access := bucket.Access
		if access == "" {
			access = "readWrite"
		}
		created := false
		if strings.EqualFold(provider, "garage") && c.garage != nil {
			info, wasCreated, err := c.garage.EnsureBucket(ctx, bucket.Name)
			if err != nil {
				return nil, fmt.Errorf("garage bucket %s: %w", bucket.Name, err)
			}
			created = wasCreated
			if err := c.garage.AllowBucketKey(ctx, info.ID, accessKey, permissionsForAccess(access)); err != nil {
				return nil, fmt.Errorf("garage bucket %s permissions: %w", bucket.Name, err)
			}
		} else {
			if err := c.CreateBucket(ctx, bucket.Name); err != nil {
				return nil, err
			}
		}
		bucketNames = append(bucketNames, bucket.Name)
		applyBucketEnv(result.Env, bucket, i)
		result.Buckets = append(result.Buckets, ProvisionedBucket{
			Name:     bucket.Name,
			Access:   access,
			Provider: provider,
			Mode:     mode,
			Created:  created,
		})
	}
	sort.Strings(bucketNames)

	result.Env["S3_ENDPOINT"] = endpointURL(c.config)
	result.Env["S3_REGION"] = c.config.Region
	result.Env["S3_PROVIDER"] = provider
	result.Env["S3_BUCKETS"] = strings.Join(bucketNames, ",")
	result.Env["AWS_ACCESS_KEY_ID"] = accessKey
	result.Env["AWS_SECRET_ACCESS_KEY"] = secretKey
	if c.config.ForcePathStyle || strings.EqualFold(provider, "garage") {
		result.Env["S3_FORCE_PATH_STYLE"] = "true"
		result.Env["AWS_S3_FORCE_PATH_STYLE"] = "true"
	}

	return result, nil
}

func (c *Client) Endpoint() string  { return c.config.Endpoint }
func (c *Client) AccessKey() string { return c.config.AccessKey }
func (c *Client) SecretKey() string { return c.config.SecretKey }

func normalizeEndpoint(endpoint string, useSSL bool) (string, bool) {
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		parsed, err := url.Parse(endpoint)
		if err == nil && parsed.Host != "" {
			return parsed.Host, parsed.Scheme == "https"
		}
	}
	return endpoint, useSSL
}

func endpointURL(cfg Config) string {
	scheme := "http"
	if cfg.UseSSL {
		scheme = "https"
	}
	return scheme + "://" + cfg.Endpoint
}

func permissionsForAccess(access string) BucketKeyPermissions {
	switch access {
	case "readOnly":
		return BucketKeyPermissions{Read: true}
	case "owner":
		return BucketKeyPermissions{Read: true, Write: true, Owner: true}
	default:
		return BucketKeyPermissions{Read: true, Write: true}
	}
}

var envUnsafeRe = regexp.MustCompile(`[^A-Za-z0-9_]+`)

func applyBucketEnv(env map[string]string, bucket model.ObjectStorageBucket, index int) {
	if index == 0 {
		env["S3_BUCKET"] = bucket.Name
		if bucket.Prefix != "" {
			env["S3_PREFIX"] = bucket.Prefix
		}
	}
	alias := bucket.Env
	if alias == "" {
		alias = strings.ToUpper(envUnsafeRe.ReplaceAllString(bucket.Name, "_"))
	}
	alias = strings.Trim(alias, "_")
	if alias == "" {
		return
	}
	env["S3_BUCKET_"+alias] = bucket.Name
	if bucket.Prefix != "" {
		env["S3_PREFIX_"+alias] = bucket.Prefix
	}
}
