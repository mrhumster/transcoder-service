package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig(t *testing.T) {
	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.NotEmpty(t, cfg.Server.StreamSeviceAddr)
	assert.NotEmpty(t, cfg.Redis.Addr)
	assert.NotEmpty(t, cfg.MinIO.Endpoint)
	assert.NotEmpty(t, cfg.MinIO.AccessKeyID)
	assert.NotEmpty(t, cfg.MinIO.SecretAccessKey)
	assert.NotEmpty(t, cfg.MinIO.BucketName)
	assert.NotEmpty(t, cfg.MinIO.Region)
}
