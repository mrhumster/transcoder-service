package storage

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

type MinIOStorage struct {
	Client MinIOClient
	Bucket string
}

func NewMinIOStorage(client MinIOClient, bucket string) *MinIOStorage {
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

// GeneratePresignedURL signs a time-limited GET url for the object. The TTL
// has to outlast the whole transcode task because ffmpeg re-seeks on the url
// for the entire playback of the source.
func (s *MinIOStorage) GeneratePresignedURL(ctx context.Context, objectName string, expires time.Duration) (string, error) {
	u, err := s.Client.PresignedGetObject(ctx, s.Bucket, objectName, expires, nil)
	if err != nil {
		return "", fmt.Errorf("error generating presigned url for %s: %w", objectName, err)
	}
	return u.String(), nil
}

// StatSize returns the object size in bytes, mirroring os.Stat for local
// files so metadata.size stays correct when the source is never downloaded.
func (s *MinIOStorage) StatSize(ctx context.Context, objectName string) (int64, error) {
	info, err := s.Client.StatObject(ctx, s.Bucket, objectName, minio.StatObjectOptions{})
	if err != nil {
		return 0, fmt.Errorf("error stat object %s: %w", objectName, err)
	}
	return info.Size, nil
}

// DownloadDir fetches every object under remoteDir into localDir. Keys are
// resolved relative to remoteDir so the local tree mirrors the remote one, and
// a traversal attempt in an object name cannot escape localDir.
func (s *MinIOStorage) DownloadDir(ctx context.Context, remoteDir, localDir string) error {
	prefix := strings.TrimSuffix(remoteDir, "/")
	if prefix != "" {
		prefix += "/"
	}

	keys, err := s.Client.ListObjectNames(ctx, s.Bucket, prefix)
	if err != nil {
		return fmt.Errorf("error listing %s: %w", prefix, err)
	}
	if len(keys) == 0 {
		return fmt.Errorf("no objects under %s", prefix)
	}

	for _, key := range keys {
		rel := strings.TrimPrefix(key, prefix)
		if rel == "" {
			continue
		}
		dest := filepath.Join(localDir, filepath.FromSlash(rel))
		// filepath.Join already cleans the path, but an object name like
		// "../../etc/passwd" would still climb out, so re-check.
		cleanRoot, err := filepath.Abs(localDir)
		if err != nil {
			return err
		}
		cleanDest, err := filepath.Abs(dest)
		if err != nil {
			return err
		}
		if cleanDest != cleanRoot && !strings.HasPrefix(cleanDest, cleanRoot+string(os.PathSeparator)) {
			return fmt.Errorf("object key %q escapes the local dir", key)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("error creating %s: %w", filepath.Dir(dest), err)
		}
		if err := s.Download(ctx, key, dest); err != nil {
			return err
		}
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
			contentType = "application/x-mpegURL"
		}
		slog.Debug("uploading segment", "key", remoteKey)
		return s.Upload(ctx, remoteKey, path, contentType)
	})
}
