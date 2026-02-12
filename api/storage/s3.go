package storage

import (
	"context"
	"fmt"
	"log"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Region    string
	UseSSL    bool
}

type Client struct {
	mc     *minio.Client
	config Config
}

func NewClient(cfg Config) (*Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}
	return &Client{mc: mc, config: cfg}, nil
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

func (c *Client) Endpoint() string {
	return c.config.Endpoint
}
