package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	pb "github.com/mrhumster/transcoder-service/gen/go/stream"
	mockProc "github.com/mrhumster/transcoder-service/internal/processor/mock"
	mockSvc "github.com/mrhumster/transcoder-service/internal/service/mock"
	mockStor "github.com/mrhumster/transcoder-service/internal/storage/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func exportTask(t *testing.T, p VideoExportPayload) *asynq.Task {
	t.Helper()
	b, err := json.Marshal(p)
	require.NoError(t, err)
	return asynq.NewTask(TaskVideoExport, b)
}

// withRendition makes the DownloadDir stub actually lay down a playlist, so the
// handler gets past the presence check and the mux/upload path is what runs.
func withRendition(m *mockStor.MockFileStorage) *mockStor.MockFileStorage {
	m.EXPECT().DownloadDir(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, localDir string) error {
			playlist := "#EXTM3U\n#EXTINF:10.0,\nseg_0.ts\n#EXT-X-ENDLIST\n"
			return os.WriteFile(filepath.Join(localDir, "index.m3u8"), []byte(playlist), 0o644)
		})
	return m
}

// fakeMuxer writes a real file so the handler's stat/size path is exercised
// instead of being stubbed out.
func fakeMuxer(t *testing.T, content string, failWith error) *mockProc.MockMuxer {
	t.Helper()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	m := mockProc.NewMockMuxer(ctrl)
	m.EXPECT().MuxToMP4(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _, out string) error {
			if failWith != nil {
				return failWith
			}
			return os.WriteFile(out, []byte(content), 0o644)
		}).AnyTimes()
	return m
}

func TestHandleVideoExport_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	streamID := uuid.New()
	payload := VideoExportPayload{StreamUUID: streamID, OwnerUUID: uuid.New(), OwnerEmail: "owner@example.com"}

	mockStorage := mockStor.NewMockFileStorage(ctrl)
	mockService := mockSvc.NewMockStreamServiceClient(ctrl)
	muxer := fakeMuxer(t, "mp4-bytes", nil)

	// The rendition is pulled from the same prefix the transcoder wrote.
	withRendition(mockStorage)
	mockStorage.EXPECT().Upload(gomock.Any(), fmt.Sprintf("processed/%s/video.mp4", streamID), gomock.Any(), "video/mp4")

	var reported *pb.CompleteStreamExportRequest
	mockService.EXPECT().CompleteStreamExport(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *pb.CompleteStreamExportRequest, _ ...any) (*pb.CompleteStreamExportResponse, error) {
			reported = req
			return &pb.CompleteStreamExportResponse{Updated: true}, nil
		})

	var notified bool
	h := NewHandleVideoExport(muxer, mockStorage, mockService, func(context.Context, VideoExportPayload, int64) error {
		notified = true
		return nil
	})

	err := h.HandleVideoExportTask(context.Background(), exportTask(t, payload))
	require.NoError(t, err)

	require.NotNil(t, reported, "the outcome must be reported even on success")
	assert.True(t, reported.Success)
	assert.Equal(t, int64(len("mp4-bytes")), reported.Size)
	assert.Equal(t, streamID.String(), reported.StreamUuid)
	assert.True(t, notified, "a ready export should notify")
}

func TestHandleVideoExport_ReportsFailureToStreamService(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	streamID := uuid.New()
	payload := VideoExportPayload{StreamUUID: streamID}

	mockStorage := mockStor.NewMockFileStorage(ctrl)
	mockService := mockSvc.NewMockStreamServiceClient(ctrl)
	muxer := fakeMuxer(t, "", fmt.Errorf("ffmpeg mux failed: exit 1"))

	withRendition(mockStorage)
	// No upload: a failed mux must not publish a partial file.

	var reported *pb.CompleteStreamExportRequest
	mockService.EXPECT().CompleteStreamExport(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *pb.CompleteStreamExportRequest, _ ...any) (*pb.CompleteStreamExportResponse, error) {
			reported = req
			return &pb.CompleteStreamExportResponse{Updated: true}, nil
		})

	h := NewHandleVideoExport(muxer, mockStorage, mockService, nil)
	err := h.HandleVideoExportTask(context.Background(), exportTask(t, payload))
	require.Error(t, err)

	require.NotNil(t, reported, "a failed export must still leave pending")
	assert.False(t, reported.Success)
	assert.Contains(t, reported.Error, "exit 1")
}

func TestHandleVideoExport_MissingPlaylistIsNotRetried(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	streamID := uuid.New()
	mockStorage := mockStor.NewMockFileStorage(ctrl)
	mockService := mockSvc.NewMockStreamServiceClient(ctrl)
	muxer := mockProc.NewMockMuxer(ctrl)

	// The download succeeds but leaves no playlist behind.
	mockStorage.EXPECT().DownloadDir(gomock.Any(), gomock.Any(), gomock.Any())

	var reported *pb.CompleteStreamExportRequest
	mockService.EXPECT().CompleteStreamExport(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *pb.CompleteStreamExportRequest, _ ...any) (*pb.CompleteStreamExportResponse, error) {
			reported = req
			return &pb.CompleteStreamExportResponse{Updated: true}, nil
		})

	h := NewHandleVideoExport(muxer, mockStorage, mockService, nil)
	err := h.HandleVideoExportTask(context.Background(), exportTask(t, VideoExportPayload{StreamUUID: streamID}))

	require.Error(t, err)
	assert.ErrorIs(t, err, asynq.SkipRetry)
	require.NotNil(t, reported)
	assert.False(t, reported.Success)
}

func TestHandleVideoExport_MissingRenditionIsNotRetried(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	streamID := uuid.New()
	mockStorage := mockStor.NewMockFileStorage(ctrl)
	mockService := mockSvc.NewMockStreamServiceClient(ctrl)
	muxer := mockProc.NewMockMuxer(ctrl)

	mockStorage.EXPECT().DownloadDir(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(fmt.Errorf("error download from storage: The specified key does not exist."))
	mockService.EXPECT().CompleteStreamExport(gomock.Any(), gomock.Any()).
		Return(&pb.CompleteStreamExportResponse{Updated: true}, nil)

	h := NewHandleVideoExport(muxer, mockStorage, mockService, nil)
	err := h.HandleVideoExportTask(context.Background(), exportTask(t, VideoExportPayload{StreamUUID: streamID}))

	require.Error(t, err)
	assert.ErrorIs(t, err, asynq.SkipRetry)
}

func TestHandleVideoExport_EmptyOutputIsRejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	streamID := uuid.New()
	mockStorage := mockStor.NewMockFileStorage(ctrl)
	mockService := mockSvc.NewMockStreamServiceClient(ctrl)
	muxer := fakeMuxer(t, "", nil)

	withRendition(mockStorage)
	// No upload for a zero-byte object.
	mockService.EXPECT().CompleteStreamExport(gomock.Any(), gomock.Any()).
		Return(&pb.CompleteStreamExportResponse{Updated: true}, nil)

	h := NewHandleVideoExport(muxer, mockStorage, mockService, nil)
	err := h.HandleVideoExportTask(context.Background(), exportTask(t, VideoExportPayload{StreamUUID: streamID}))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty file")
}

func TestHandleVideoExport_NotificationFailureDoesNotFailTheExport(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	streamID := uuid.New()
	mockStorage := mockStor.NewMockFileStorage(ctrl)
	mockService := mockSvc.NewMockStreamServiceClient(ctrl)
	muxer := fakeMuxer(t, "mp4-bytes", nil)

	withRendition(mockStorage)
	mockStorage.EXPECT().Upload(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any())
	mockService.EXPECT().CompleteStreamExport(gomock.Any(), gomock.Any()).
		Return(&pb.CompleteStreamExportResponse{Updated: true}, nil)

	h := NewHandleVideoExport(muxer, mockStorage, mockService, func(context.Context, VideoExportPayload, int64) error {
		return fmt.Errorf("mailer queue unreachable")
	})

	// The row is already ready and the file is uploaded, so a mailer outage must
	// not be retried as if the export itself had failed.
	err := h.HandleVideoExportTask(context.Background(), exportTask(t, VideoExportPayload{StreamUUID: streamID}))
	require.NoError(t, err)
}

func TestHandleVideoExport_WorkDirIsCleanedUp(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	streamID := uuid.New()
	mockStorage := mockStor.NewMockFileStorage(ctrl)
	mockService := mockSvc.NewMockStreamServiceClient(ctrl)
	muxer := fakeMuxer(t, "mp4-bytes", nil)

	withRendition(mockStorage)
	mockStorage.EXPECT().Upload(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any())
	mockService.EXPECT().CompleteStreamExport(gomock.Any(), gomock.Any()).
		Return(&pb.CompleteStreamExportResponse{Updated: true}, nil)

	h := NewHandleVideoExport(muxer, mockStorage, mockService, nil)
	require.NoError(t, h.HandleVideoExportTask(context.Background(), exportTask(t, VideoExportPayload{StreamUUID: streamID})))

	_, err := os.Stat(filepath.Join(os.TempDir(), "export-"+streamID.String()))
	assert.True(t, os.IsNotExist(err), "work dir must not outlive the task")
}

func TestHandleVideoExport_RejectsUnusablePayloads(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mockStor.NewMockFileStorage(ctrl)
	mockService := mockSvc.NewMockStreamServiceClient(ctrl)
	h := NewHandleVideoExport(mockProc.NewMockMuxer(ctrl), mockStorage, mockService, nil)

	t.Run("not json", func(t *testing.T) {
		err := h.HandleVideoExportTask(context.Background(), asynq.NewTask(TaskVideoExport, []byte("{")))
		require.Error(t, err)
		assert.ErrorIs(t, err, asynq.SkipRetry)
	})

	t.Run("missing stream id", func(t *testing.T) {
		err := h.HandleVideoExportTask(context.Background(), exportTask(t, VideoExportPayload{}))
		require.Error(t, err)
		assert.ErrorIs(t, err, asynq.SkipRetry)
	})
}

func TestHandleVideoExport_TruncatesReportedError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	streamID := uuid.New()
	mockStorage := mockStor.NewMockFileStorage(ctrl)
	mockService := mockSvc.NewMockStreamServiceClient(ctrl)
	muxer := fakeMuxer(t, "", fmt.Errorf("%s", strings.Repeat("x", 5000)))

	withRendition(mockStorage)

	var reported *pb.CompleteStreamExportRequest
	mockService.EXPECT().CompleteStreamExport(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *pb.CompleteStreamExportRequest, _ ...any) (*pb.CompleteStreamExportResponse, error) {
			reported = req
			return &pb.CompleteStreamExportResponse{Updated: true}, nil
		})

	h := NewHandleVideoExport(muxer, mockStorage, mockService, nil)
	require.Error(t, h.HandleVideoExportTask(context.Background(), exportTask(t, VideoExportPayload{StreamUUID: streamID})))

	require.NotNil(t, reported)
	// The row text is handed to the browser, so it cannot grow without bound.
	// The ellipsis marker is multi-byte, so budget for it explicitly.
	assert.LessOrEqual(t, len(reported.Error), exportMaxErrorLen+len("…"))
	assert.Less(t, len(reported.Error), 5000)
}
