package storage

import "github.com/minio/minio-go/v7"

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
