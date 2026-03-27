package storage

import (
	"context"
	"fmt"
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
