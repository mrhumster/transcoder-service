//go:generate mockgen -source=filestorage.go -destination=mock/filestorage_mock.go -package=mock
package storage

import (
	"context"
	"time"
)

type FileStorage interface {
	Download(ctx context.Context, remoteKey, localPath string) error
	// GeneratePresignedURL returns a time-limited GET url for the object so a
	// consumer (ffprobe/ffmpeg) can read it straight from the storage with
	// Range seeks instead of pulling the whole file onto local disk.
	GeneratePresignedURL(ctx context.Context, remoteKey string, expires time.Duration) (string, error)
	// StatSize returns the object size in bytes without downloading it.
	StatSize(ctx context.Context, remoteKey string) (int64, error)
	// DownloadDir mirrors every object under remoteDir into localDir, keeping
	// the relative layout. It is the read counterpart of UploadDir and is what
	// lets the export worker mux an existing HLS rendition without a second
	// transcode.
	DownloadDir(ctx context.Context, remoteDir, localDir string) error
	Upload(ctx context.Context, remoteKey, localPath, contentType string) error
	UploadDir(ctx context.Context, remoteDir, localDir string) error
}
