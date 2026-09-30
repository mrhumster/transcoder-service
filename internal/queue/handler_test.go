package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/mrhumster/transcoder-service/gen/go/stream"
	"github.com/mrhumster/transcoder-service/internal/processor"
	mockProc "github.com/mrhumster/transcoder-service/internal/processor/mock"
	mockSvc "github.com/mrhumster/transcoder-service/internal/service/mock"
	mockStor "github.com/mrhumster/transcoder-service/internal/storage/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// closedProgressChans returns both channels already closed, which is what the
// transcode handler needs to fall out of its select loop and reach the upload
// phase. Returning nil (as these tests used to) leaves the handler parked on two
// nil channels forever, and gomock rejects a single value for a two-result call.
func closedProgressChans() (<-chan processor.Progress, <-chan error) {
	prog := make(chan processor.Progress)
	errs := make(chan error)
	close(prog)
	close(errs)
	return prog, errs
}

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
		mockProcessor.EXPECT().
			ProbeMetadata(gomock.Any(), gomock.Any()).
			Return(processor.VideoMetadata{}, nil)
		mockService.EXPECT().
			UpdateStreamMetadata(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamMetadataResponse{}, nil)
		progChan, errChan := closedProgressChans()
		mockProcessor.EXPECT().
			TranscodeToHLS(
				gomock.Any(),
				gomock.Any(),
				gomock.Any()).
			Return(progChan, errChan)
		// The handler reports the upload phase and the final state through
		// UpdateStreamProcessing; these blocks do not assert on it.
		mockService.EXPECT().
			UpdateStreamProcessing(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamProcessingResponse{Updated: true}, nil).
			AnyTimes()
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
		mockProcessor.EXPECT().
			ProbeMetadata(gomock.Any(), gomock.Any()).
			Return(processor.VideoMetadata{}, nil)
		mockService.EXPECT().
			UpdateStreamMetadata(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamMetadataResponse{}, nil)
		progChan, errChan := closedProgressChans()
		mockProcessor.EXPECT().
			TranscodeToHLS(
				gomock.Any(),
				gomock.Any(),
				gomock.Any()).
			Return(progChan, errChan)
		// The handler reports the upload phase and the final state through
		// UpdateStreamProcessing; these blocks do not assert on it.
		mockService.EXPECT().
			UpdateStreamProcessing(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamProcessingResponse{Updated: true}, nil).
			AnyTimes()
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
		mockProcessor.EXPECT().
			ProbeMetadata(gomock.Any(), gomock.Any()).
			Return(processor.VideoMetadata{}, nil)
		mockService.EXPECT().
			UpdateStreamMetadata(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamMetadataResponse{}, nil)
		progChan, errChan := closedProgressChans()
		mockProcessor.EXPECT().
			TranscodeToHLS(
				gomock.Any(),
				gomock.Any(),
				gomock.Any()).
			Return(progChan, errChan)
		// The handler reports the upload phase and the final state through
		// UpdateStreamProcessing; these blocks do not assert on it.
		mockService.EXPECT().
			UpdateStreamProcessing(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamProcessingResponse{Updated: true}, nil).
			AnyTimes()
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
		mockProcessor.EXPECT().
			ProbeMetadata(gomock.Any(), gomock.Any()).
			Return(processor.VideoMetadata{}, nil)
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
		mockProcessor.EXPECT().
			ProbeMetadata(gomock.Any(), gomock.Any()).
			Return(processor.VideoMetadata{}, nil)
		mockService.EXPECT().
			UpdateStreamMetadata(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamMetadataResponse{}, nil)
		// A transcode failure arrives on the error channel; the method itself
		// returns channels, so the error cannot be returned from the call.
		failProg := make(chan processor.Progress)
		failErrs := make(chan error, 1)
		failErrs <- fmt.Errorf("processing error")
		close(failProg)
		close(failErrs)
		mockProcessor.EXPECT().
			TranscodeToHLS(
				gomock.Any(),
				gomock.Any(),
				gomock.Any()).
			Return(failProg, failErrs)
		mockService.EXPECT().
			UpdateStreamProcessing(
				gomock.Any(),
				gomock.Any()).
			Return(&stream.UpdateStreamProcessingResponse{Updated: true}, nil).
			AnyTimes()
		err := handler.HandleVideoTranscoderTask(ctx, task)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "processing error")
	})
}
