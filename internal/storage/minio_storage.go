package storage

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"

	"github.com/minio/minio-go/v7"
)

type MinIOStorage struct {
	Client MinIOClient
	Bucket string
}

func NewMinIOStorage(client *minio.Client, bucket string) *MinIOStorage {
	return &MinIOStorage{
		Client: client,
		Bucket: bucket,
	}
}

func (s *MinIOStorage) Download(ctx context.Context, objectName, filePath string) error {
	if err := s.Client.FGetObject(ctx, s.Bucket, objectName, filePath, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("error download from storage: %w", err)
	}
	return nil
}

func (s *MinIOStorage) Upload(ctx context.Context, objectName, filePath, contentType string) error {
	uploadInfo, err := s.Client.FPutObject(ctx, s.Bucket, objectName, filePath, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("error uploading to bucket %s %v", s.Bucket, err)
	}
	slog.Info("Upload success", "Bucket", s.Bucket, "uploadInfo", uploadInfo)
	return nil
}

func (s *MinIOStorage) UploadDir(ctx context.Context, remoteDir, localDir string) error {
	return filepath.WalkDir(localDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(localDir, path)
		if err != nil {
			return err
		}
		remoteKey := filepath.Join(remoteDir, relPath)
		contentType := "video/MP2T"
		if filepath.Ext(path) == ".m3u8" {
			contentType = "video/x-mpegURL"
		}
		slog.Debug("uploading segment", "key", remoteKey)
		return s.Upload(ctx, remoteKey, path, contentType)
	})
}
