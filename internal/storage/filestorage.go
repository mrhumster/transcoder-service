//go:generate mockgen -source=filestorage.go -destination=mock/filestorage_mock.go -package=mock
package storage

import "context"

type FileStorage interface {
	Download(ctx context.Context, remoteKey, localPath string) error
	// DownloadDir mirrors every object under remoteDir into localDir, keeping
	// the relative layout. It is the read counterpart of UploadDir and is what
	// lets the export worker mux an existing HLS rendition without a second
	// transcode.
	DownloadDir(ctx context.Context, remoteDir, localDir string) error
	Upload(ctx context.Context, remoteKey, localPath, contentType string) error
	UploadDir(ctx context.Context, remoteDir, localDir string) error
}
