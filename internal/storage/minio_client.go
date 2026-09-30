//go:generate mockgen -source=minio_client.go -destination=mock/minio_client_mock.go -package=mock
package storage

import (
	"context"

	"github.com/minio/minio-go/v7"
)

type MinIOClient interface {
	FGetObject(ctx context.Context, bucketName, objectName, filePath string, opts minio.GetObjectOptions) error
	FPutObject(ctx context.Context, bucketName, objectName, filePath string, opts minio.PutObjectOptions) (minio.UploadInfo, error)
	// ListObjectNames returns the object keys under prefix. The real client
	// streams them through a channel, so the adapter flattens it to a slice:
	// callers here work with whole HLS directories, which are small and need to
	// be enumerated completely anyway.
	ListObjectNames(ctx context.Context, bucketName, prefix string) ([]string, error)
}
