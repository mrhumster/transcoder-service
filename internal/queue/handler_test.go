package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/mrhumster/transcoder-service/gen/go/stream"
	mockProc "github.com/mrhumster/transcoder-service/internal/processor/mock"
	mockSvc "github.com/mrhumster/transcoder-service/internal/service/mock"
	mockStor "github.com/mrhumster/transcoder-service/internal/storage/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestHandle_HandleVideoTranscoderTask(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockProcessor := mockProc.NewMockVideoProcessor(ctrl)
		mockStorage := mockStor.NewMockFileStorage(ctrl)
		mockService := mockSvc.NewMockStreamServiceClient(ctrl)
		handler := NewHandleVideoTranscoder(
			mockProcessor,
			mockStorage,
			mockService,
		)
		ctx := context.Background()
		streamUUID := uuid.New()
		payload := VideoTranscodingPayload{
			StreamUUID: streamUUID,
			InputPath:  "raw/video.mp4",
		}
		payloadBytes, _ := json.Marshal(payload)
		task := asynq.NewTask(
			TaskVideoTranscoding,
			payloadBytes,
		)
		mockStorage.EXPECT().
			Download(
				gomock.Any(),
				payload.InputPath,
				gomock.Any()).
			Return(nil)
		mockService.EXPECT().
			UpdateStreamMetadata(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamMetadataResponse{}, nil)
		mockProcessor.EXPECT().
			TranscodeToHLS(
				gomock.Any(),
				gomock.Any(),
				gomock.Any()).
			Return(nil)
		mockStorage.EXPECT().
			UploadDir(
				gomock.Any(),
				gomock.Any(),
				gomock.Any()).
			Return(nil)
		mockService.EXPECT().
			UpdateStreamStatus(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamStatusResponse{}, nil)
		err := handler.HandleVideoTranscoderTask(ctx, task)
		require.NoError(t, err)
	})

	t.Run("download storage error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockProcessor := mockProc.NewMockVideoProcessor(ctrl)
		mockStorage := mockStor.NewMockFileStorage(ctrl)
		mockService := mockSvc.NewMockStreamServiceClient(ctrl)
		handler := NewHandleVideoTranscoder(
			mockProcessor,
			mockStorage,
			mockService,
		)
		ctx := context.Background()
		streamUUID := uuid.New()
		payload := VideoTranscodingPayload{
			StreamUUID: streamUUID,
			InputPath:  "raw/video.mp4",
		}
		payloadBytes, _ := json.Marshal(payload)
		task := asynq.NewTask(
			TaskVideoTranscoding,
			payloadBytes,
		)
		mockStorage.EXPECT().
			Download(
				gomock.Any(),
				payload.InputPath,
				gomock.Any()).
			Return(fmt.Errorf("download error"))
		err := handler.HandleVideoTranscoderTask(ctx, task)
		require.NoError(t, err)
	})
	t.Run("upload storage error propagation", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockProcessor := mockProc.NewMockVideoProcessor(ctrl)
		mockStorage := mockStor.NewMockFileStorage(ctrl)
		mockService := mockSvc.NewMockStreamServiceClient(ctrl)
		handler := NewHandleVideoTranscoder(
			mockProcessor,
			mockStorage,
			mockService,
		)
		ctx := context.Background()
		streamUUID := uuid.New()
		payload := VideoTranscodingPayload{
			StreamUUID: streamUUID,
			InputPath:  "raw/video.mp4",
		}
		payloadBytes, _ := json.Marshal(payload)
		task := asynq.NewTask(
			TaskVideoTranscoding,
			payloadBytes,
		)
		mockStorage.EXPECT().
			Download(
				gomock.Any(),
				payload.InputPath,
				gomock.Any()).
			Return(nil)
		mockService.EXPECT().
			UpdateStreamMetadata(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamMetadataResponse{}, nil)
		mockProcessor.EXPECT().
			TranscodeToHLS(
				gomock.Any(),
				gomock.Any(),
				gomock.Any()).
			Return(nil)
		mockStorage.EXPECT().
			UploadDir(
				gomock.Any(),
				gomock.Any(),
				gomock.Any()).
			Return(fmt.Errorf("upload error"))
		err := handler.HandleVideoTranscoderTask(ctx, task)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "upload error")
	})
	t.Run("update stream status error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockProcessor := mockProc.NewMockVideoProcessor(ctrl)
		mockStorage := mockStor.NewMockFileStorage(ctrl)
		mockService := mockSvc.NewMockStreamServiceClient(ctrl)
		handler := NewHandleVideoTranscoder(
			mockProcessor,
			mockStorage,
			mockService,
		)
		ctx := context.Background()
		streamUUID := uuid.New()
		payload := VideoTranscodingPayload{
			StreamUUID: streamUUID,
			InputPath:  "raw/video.mp4",
		}
		payloadBytes, _ := json.Marshal(payload)
		task := asynq.NewTask(
			TaskVideoTranscoding,
			payloadBytes,
		)
		mockStorage.EXPECT().
			Download(
				gomock.Any(),
				payload.InputPath,
				gomock.Any()).
			Return(nil)
		mockService.EXPECT().
			UpdateStreamMetadata(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamMetadataResponse{}, nil)
		mockProcessor.EXPECT().
			TranscodeToHLS(
				gomock.Any(),
				gomock.Any(),
				gomock.Any()).
			Return(nil)
		mockStorage.EXPECT().
			UploadDir(
				gomock.Any(),
				gomock.Any(),
				gomock.Any()).
			Return(nil)
		mockService.EXPECT().
			UpdateStreamStatus(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamStatusResponse{}, fmt.Errorf("update status error"))
		err := handler.HandleVideoTranscoderTask(ctx, task)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "update status error")
	})
	t.Run("update stream metadata error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockProcessor := mockProc.NewMockVideoProcessor(ctrl)
		mockStorage := mockStor.NewMockFileStorage(ctrl)
		mockService := mockSvc.NewMockStreamServiceClient(ctrl)
		handler := NewHandleVideoTranscoder(
			mockProcessor,
			mockStorage,
			mockService,
		)
		ctx := context.Background()
		streamUUID := uuid.New()
		payload := VideoTranscodingPayload{
			StreamUUID: streamUUID,
			InputPath:  "raw/video.mp4",
		}
		payloadBytes, _ := json.Marshal(payload)
		task := asynq.NewTask(
			TaskVideoTranscoding,
			payloadBytes,
		)
		mockStorage.EXPECT().
			Download(
				gomock.Any(),
				payload.InputPath,
				gomock.Any()).
			Return(nil)
		mockService.EXPECT().
			UpdateStreamMetadata(
				gomock.Any(),
				gomock.Any()).
			Return(nil, fmt.Errorf("update metadata error"))
		err := handler.HandleVideoTranscoderTask(ctx, task)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "update metadata error")
	})
	t.Run("processor error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockProcessor := mockProc.NewMockVideoProcessor(ctrl)
		mockStorage := mockStor.NewMockFileStorage(ctrl)
		mockService := mockSvc.NewMockStreamServiceClient(ctrl)
		handler := NewHandleVideoTranscoder(
			mockProcessor,
			mockStorage,
			mockService,
		)
		ctx := context.Background()
		streamUUID := uuid.New()
		payload := VideoTranscodingPayload{
			StreamUUID: streamUUID,
			InputPath:  "raw/video.mp4",
		}
		payloadBytes, _ := json.Marshal(payload)
		task := asynq.NewTask(
			TaskVideoTranscoding,
			payloadBytes,
		)
		mockStorage.EXPECT().
			Download(
				gomock.Any(),
				payload.InputPath,
				gomock.Any()).
			Return(nil)
		mockService.EXPECT().
			UpdateStreamMetadata(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamMetadataResponse{}, nil)
		mockProcessor.EXPECT().
			TranscodeToHLS(
				gomock.Any(),
				gomock.Any(),
				gomock.Any()).
			Return(fmt.Errorf("processing error"))
		err := handler.HandleVideoTranscoderTask(ctx, task)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "processing error")
	})
}
