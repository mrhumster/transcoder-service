package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/mrhumster/transcoder-service/internal/storage/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMinioStorage_Download(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockMinioClient := mock.NewMockMinIOClient(ctrl)
		bucketName := "files"
		minioStorage := NewMinIOStorage(mockMinioClient, bucketName)
		ctx := context.Background()
		objectName := "index.m3u8"
		filePath := "/tmp/index.m3u8"
		mockMinioClient.EXPECT().
			FGetObject(
				gomock.Any(),
				bucketName,
				objectName,
				filePath,
				gomock.Any()).
			Return(nil)
		err := minioStorage.Download(ctx, objectName, filePath)
		require.NoError(t, err)
	})

	t.Run("client error propagation", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockMinioClient := mock.NewMockMinIOClient(ctrl)
		bucketName := "files"
		minioStorage := NewMinIOStorage(mockMinioClient, bucketName)
		ctx := context.Background()
		objectName := "index.m3u8"
		filePath := "/tmp/index.m3u8"
		mockMinioClient.EXPECT().
			FGetObject(
				gomock.Any(),
				bucketName,
				objectName,
				filePath,
				gomock.Any()).
			Return(fmt.Errorf("client error"))
		err := minioStorage.Download(ctx, objectName, filePath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "client error")
	})
}

func TestMinioStorage_Upload(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockMinioClient := mock.NewMockMinIOClient(ctrl)
		bucketName := "files"
		minioStorage := NewMinIOStorage(mockMinioClient, bucketName)
		ctx := context.Background()
		objectName := "index.m3u8"
		filePath := "/tmp/index.m3u8"
		mockMinioClient.EXPECT().
			FPutObject(
				gomock.Any(),
				bucketName,
				objectName,
				filePath,
				gomock.Any()).
			Return(minio.UploadInfo{}, nil)
		err := minioStorage.Upload(ctx, objectName, filePath, "application/x-mpegURL")
		require.NoError(t, err)
	})
	t.Run("client error propagation", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockMinioClient := mock.NewMockMinIOClient(ctrl)
		bucketName := "files"
		minioStorage := NewMinIOStorage(mockMinioClient, bucketName)
		ctx := context.Background()
		objectName := "index.m3u8"
		filePath := "/tmp/index.m3u8"
		mockMinioClient.EXPECT().
			FPutObject(
				gomock.Any(),
				bucketName,
				objectName,
				filePath,
				gomock.Any()).
			Return(minio.UploadInfo{}, fmt.Errorf("minio error"))
		err := minioStorage.Upload(ctx, objectName, filePath, "application/x-mpegURL")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "minio error")
	})
}

func TestMinioStorage_DownloadDir(t *testing.T) {
	t.Run("mirrors the remote layout", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockMinioClient := mock.NewMockMinIOClient(ctrl)
		bucketName := "files"
		minioStorage := NewMinIOStorage(mockMinioClient, bucketName)
		localDir := t.TempDir()

		mockMinioClient.EXPECT().
			ListObjectNames(gomock.Any(), bucketName, "processed/abc/").
			Return([]string{"processed/abc/index.m3u8", "processed/abc/seg_0.ts", "processed/abc/extra/seg_1.ts"}, nil)
		for _, key := range []string{"processed/abc/index.m3u8", "processed/abc/seg_0.ts", "processed/abc/extra/seg_1.ts"} {
			mockMinioClient.EXPECT().
				FGetObject(gomock.Any(), bucketName, key, gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, _, _, filePath string, _ minio.GetObjectOptions) error {
					return os.WriteFile(filePath, []byte("data"), 0o644)
				})
		}

		require.NoError(t, minioStorage.DownloadDir(context.Background(), "processed/abc", localDir))

		for _, rel := range []string{"index.m3u8", "seg_0.ts", "extra/seg_1.ts"} {
			_, err := os.Stat(filepath.Join(localDir, rel))
			assert.NoError(t, err, "expected %s to be mirrored", rel)
		}
	})

	t.Run("skips directory placeholders", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockMinioClient := mock.NewMockMinIOClient(ctrl)
		bucketName := "files"
		minioStorage := NewMinIOStorage(mockMinioClient, bucketName)
		localDir := t.TempDir()

		mockMinioClient.EXPECT().
			ListObjectNames(gomock.Any(), bucketName, "processed/abc/").
			Return([]string{"processed/abc/", "processed/abc/index.m3u8"}, nil)
		mockMinioClient.EXPECT().
			FGetObject(gomock.Any(), bucketName, "processed/abc/index.m3u8", gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, _, _, filePath string, _ minio.GetObjectOptions) error {
				return os.WriteFile(filePath, []byte("data"), 0o644)
			})

		require.NoError(t, minioStorage.DownloadDir(context.Background(), "processed/abc", localDir))
		// The placeholder must not become a file named after the prefix.
		_, err := os.Stat(filepath.Join(localDir, "processed"))
		assert.True(t, os.IsNotExist(err))
	})

	t.Run("rejects a key that escapes the local dir", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockMinioClient := mock.NewMockMinIOClient(ctrl)
		bucketName := "files"
		minioStorage := NewMinIOStorage(mockMinioClient, bucketName)
		localDir := t.TempDir()

		mockMinioClient.EXPECT().
			ListObjectNames(gomock.Any(), bucketName, "processed/abc/").
			Return([]string{"processed/abc/../../../../etc/passwd"}, nil)

		err := minioStorage.DownloadDir(context.Background(), "processed/abc", localDir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "escapes the local dir")
	})

	t.Run("empty prefix is an error, not a silent success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockMinioClient := mock.NewMockMinIOClient(ctrl)
		bucketName := "files"
		minioStorage := NewMinIOStorage(mockMinioClient, bucketName)

		mockMinioClient.EXPECT().
			ListObjectNames(gomock.Any(), bucketName, "processed/gone/").
			Return(nil, nil)

		err := minioStorage.DownloadDir(context.Background(), "processed/gone", t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no objects")
	})
}
