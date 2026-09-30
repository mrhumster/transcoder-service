package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	sharedconfig "github.com/mrhumster/go-shared/config"
)

// client adapts *minio.Client to MinIOClient by flattening the streaming list
// API into a slice.
type client struct {
	*minio.Client
}

func (c client) ListObjectNames(ctx context.Context, bucketName, prefix string) ([]string, error) {
	var names []string
	for obj := range c.Client.ListObjects(ctx, bucketName, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		// Directory placeholders come back as keys ending in a slash.
		if obj.Key == "" || strings.HasSuffix(obj.Key, "/") {
			continue
		}
		names = append(names, obj.Key)
	}
	return names, nil
}

func NewMinIOStorageFromConfig(cfg sharedconfig.MinIO) (*MinIOStorage, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create MinIO client: %w", err)
	}

	return NewMinIOStorage(client{Client: mc}, cfg.BucketName), nil
}
