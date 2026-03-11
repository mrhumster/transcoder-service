//go:generate mockgen -source=filestorage.go -destination=mock/filestorage_mock.go -package=mock
package storage

import "context"

type FileStorage interface {
	Download(ctx context.Context, remoteKey, localPath string) error
	Upload(ctx context.Context, remoteKey, localPath, contentType string) error
}
