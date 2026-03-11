//go:generate mockgen -source=minio_client.go -destination=mock/minio_client_mock.go -package=mock
package storage

import (
	"context"

	"github.com/minio/minio-go/v7"
)

type MinIOClient interface {
	FGetObject(ctx context.Context, bucketName, objectName, filePath string, opts minio.GetObjectOptions) error
	FPutObject(ctx context.Context, bucketName, objectNmae, filePath string, opts minio.PutObjectOptions) (minio.UploadInfo, error)
}
