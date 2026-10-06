package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestChunkCountAndLengths(t *testing.T) {
	old := chunkDurationSec
	chunkDurationSec = 1800
	t.Cleanup(func() { chunkDurationSec = old })

	cases := []struct {
		dur  float64
		want int
	}{
		{0, 1}, {1, 1}, {1799, 1}, {1800, 1},
		{1801, 2}, {3600, 2}, {3601, 3},
		{7200, 4}, {7201, 5},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, chunkCount(c.dur), "chunkCount(%v)", c.dur)
	}

	// The per-chunk windows must tile the source exactly.
	dur := 3601.0
	n := chunkCount(dur)
	var sum float64
	for i := 0; i < n; i++ {
		sum += chunkLenSec(i, dur)
	}
	assert.InDelta(t, dur, sum, 1e-9, "chunk lengths must cover the source")
	assert.Equal(t, 1800.0, chunkLenSec(0, dur))
	assert.Equal(t, 1800.0, chunkLenSec(1, dur))
	assert.Equal(t, 1.0, chunkLenSec(2, dur))
	assert.Equal(t, 0.0, chunkLenSec(5, dur), "a chunk past the end has no window")
}

func TestParseChunkPlaylist(t *testing.T) {
	dir := t.TempDir()

	valid := filepath.Join(dir, "index.m3u8")
	require.NoError(t, os.WriteFile(valid, []byte(
		"#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:17\n"+
			"#EXT-X-MEDIA-SEQUENCE:0\n"+
			"#EXTINF:16.666667,\nseg_0.ts\n"+
			"#EXTINF:3.333333,\nseg_1.ts\n"+
			"#EXT-X-ENDLIST\n"), 0o644))

	segs, err := parseChunkPlaylist(valid)
	require.NoError(t, err)
	require.Len(t, segs, 2)
	assert.Equal(t, "seg_0.ts", segs[0].File)
	assert.Equal(t, "16.666667", segs[0].Extinf)
	assert.Equal(t, "seg_1.ts", segs[1].File)
	assert.Equal(t, "3.333333", segs[1].Extinf)

	orphan := filepath.Join(dir, "orphan.m3u8")
	require.NoError(t, os.WriteFile(orphan, []byte("#EXTM3U\nseg_0.ts\n"), 0o644))
	_, err = parseChunkPlaylist(orphan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without #EXTINF")

	empty := filepath.Join(dir, "empty.m3u8")
	require.NoError(t, os.WriteFile(empty, []byte("#EXTM3U\n#EXT-X-ENDLIST\n"), 0o644))
	_, err = parseChunkPlaylist(empty)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no segments")

	_, err = parseChunkPlaylist(filepath.Join(dir, "missing.m3u8"))
	require.Error(t, err)
}

func TestStitchPlaylists(t *testing.T) {
	entries := [][]chunkSegment{
		{
			{File: "seg_0.ts", Extinf: "16.666667"},
			{File: "seg_1.ts", Extinf: "3.333333"},
		},
		{{File: "seg_0.ts", Extinf: "9.9"}},
	}

	want := "#EXTM3U\n" +
		"#EXT-X-VERSION:3\n" +
		"#EXT-X-TARGETDURATION:17\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXTINF:16.666667,\nchunks/c0/seg_0.ts\n" +
		"#EXTINF:3.333333,\nchunks/c0/seg_1.ts\n" +
		"#EXT-X-DISCONTINUITY\n" +
		"#EXTINF:9.9,\nchunks/c1/seg_0.ts\n" +
		"#EXT-X-ENDLIST\n"
	assert.Equal(t, want, stitchPlaylists(entries))

	// A single-chunk rendition (all chunks resumed) has no boundary.
	single := stitchPlaylists(entries[:1])
	assert.NotContains(t, single, "#EXT-X-DISCONTINUITY")
	assert.Equal(t, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:17\n"+
		"#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:16.666667,\nchunks/c0/seg_0.ts\n"+
		"#EXTINF:3.333333,\nchunks/c0/seg_1.ts\n#EXT-X-ENDLIST\n", single)
}

func TestChunkMarkerRoundtrip(t *testing.T) {
	m := chunkMarker{
		Version:  1,
		Segments: []chunkSegment{{File: "seg_0.ts", Extinf: "1.5"}},
	}
	data, err := json.Marshal(m)
	require.NoError(t, err)

	var back chunkMarker
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, m, back)
}

// writeTestChunkOutput leaves exactly what ffmpeg would leave in outputDir:
// a chunk playlist plus its segment files.
func writeTestChunkOutput(t *testing.T, outputDir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(outputDir, 0o755))
	playlist := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:10\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:9.9,\nseg_0.ts\n" +
		"#EXTINF:0.6,\nseg_1.ts\n#EXT-X-ENDLIST\n"
	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "index.m3u8"), []byte(playlist), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "seg_0.ts"), []byte("ts0"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "seg_1.ts"), []byte("ts1"), 0o644))
}

// chunkedTestEnv wires the common gomock expectations of a chunked-path test:
// presign + probe + metadata + the always-allowed progress updates.
func chunkedTestEnv(t *testing.T, ctrl *gomock.Controller, streamUUID uuid.UUID, inputPath string, duration float64) (*mockProc.MockVideoProcessor, *mockStor.MockFileStorage, *mockSvc.MockStreamServiceClient, *HandleVideoTrancoder) {
	t.Helper()
	proc := mockProc.NewMockVideoProcessor(ctrl)
	stor := mockStor.NewMockFileStorage(ctrl)
	svc := mockSvc.NewMockStreamServiceClient(ctrl)

	stor.EXPECT().
		GeneratePresignedURL(gomock.Any(), inputPath, gomock.Any()).
		Return("https://minio.local/bucket/"+inputPath+"?sig=x", nil)
	proc.EXPECT().
		ProbeMetadata(gomock.Any(), gomock.Any()).
		Return(processor.VideoMetadata{Duration: duration}, nil)
	stor.EXPECT().
		StatSize(gomock.Any(), inputPath).
		Return(int64(1), nil)
	svc.EXPECT().
		UpdateStreamMetadata(gomock.Any(), gomock.Any()).
		Return(&stream.UpdateStreamMetadataResponse{}, nil)
	svc.EXPECT().
		UpdateStreamProcessing(gomock.Any(), gomock.Any()).
		Return(&stream.UpdateStreamProcessingResponse{Updated: true}, nil).
		AnyTimes()
	svc.EXPECT().
		UpdateStreamStatus(gomock.Any(), gomock.Any()).
		Return(&stream.UpdateStreamStatusResponse{}, nil).
		AnyTimes()

	return proc, stor, svc, NewHandleVideoTranscoder(proc, stor, svc)
}

func TestHandle_ChunkedSuccess(t *testing.T) {
	ctrl := gomock.NewController(t)
	streamUUID := uuid.New()
	payload := VideoTranscodingPayload{StreamUUID: streamUUID, InputPath: "raw/video.mp4"}
	task := asynq.NewTask(TaskVideoTranscoding, mustMarshalPayload(t, payload))
	// 3600s over an 1800s threshold = 2 chunks.
	proc, stor, _, handler := chunkedTestEnv(t, ctrl, streamUUID, payload.InputPath, 3600)

	stor.EXPECT().StatSize(gomock.Any(), chunkMarkerKey(streamUUID, 0)).
		Return(int64(0), fmt.Errorf("not found"))
	stor.EXPECT().StatSize(gomock.Any(), chunkMarkerKey(streamUUID, 1)).
		Return(int64(0), fmt.Errorf("not found"))

	proc.EXPECT().
		TranscodeChunk(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, in, out string, start, dur float64) (<-chan processor.Progress, <-chan error) {
			writeTestChunkOutput(t, out)
			return closedProgressChans()
		}).
		Times(2)

	stor.EXPECT().
		UploadDir(gomock.Any(), fmt.Sprintf("processed/%s/chunks/c0", streamUUID), gomock.Any()).
		Return(nil)
	stor.EXPECT().
		UploadDir(gomock.Any(), fmt.Sprintf("processed/%s/chunks/c1", streamUUID), gomock.Any()).
		Return(nil)

	var gotIndex string
	markerUploads := 0
	stor.EXPECT().
		Upload(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, key, local, contentType string) error {
			switch {
			case strings.HasSuffix(key, "index.m3u8"):
				data, err := os.ReadFile(local)
				if err != nil {
					return err
				}
				gotIndex = string(data)
			case strings.Contains(key, "/chunks/done_"):
				markerUploads++
			default:
				return fmt.Errorf("unexpected upload key %q", key)
			}
			return nil
		}).
		Times(3)

	require.NoError(t, handler.HandleVideoTranscoderTask(context.Background(), task))

	require.Equal(t, 2, markerUploads, "both chunks must leave a checkpoint")
	assert.Contains(t, gotIndex, "chunks/c0/seg_0.ts")
	assert.Contains(t, gotIndex, "chunks/c1/seg_0.ts")
	assert.Equal(t, 1, strings.Count(gotIndex, "#EXT-X-DISCONTINUITY"))
	assert.Contains(t, gotIndex, "#EXT-X-TARGETDURATION:10")
	assert.Contains(t, gotIndex, "#EXT-X-ENDLIST")

	// The per-chunk playlist is an implementation detail and must stay out
	// of the rendition: UploadDir only ever sees a dir without index.m3u8.
	// (asserted through the local dir the second chunk cleaned up)
	_, err := os.Stat(fmt.Sprintf("/tmp/%s", streamUUID))
	assert.True(t, os.IsNotExist(err), "work dir must be removed after the task")
}

func TestHandle_ChunkedResumeSkipsCompletedChunks(t *testing.T) {
	ctrl := gomock.NewController(t)
	streamUUID := uuid.New()
	payload := VideoTranscodingPayload{StreamUUID: streamUUID, InputPath: "raw/video.mp4"}
	task := asynq.NewTask(TaskVideoTranscoding, mustMarshalPayload(t, payload))
	proc, stor, _, handler := chunkedTestEnv(t, ctrl, streamUUID, payload.InputPath, 3600)

	// Chunk 0 already completed on a previous attempt: the marker is there.
	doneMarker, err := json.Marshal(chunkMarker{
		Version:  1,
		Segments: []chunkSegment{{File: "seg_0.ts", Extinf: "9.9"}},
	})
	require.NoError(t, err)
	stor.EXPECT().StatSize(gomock.Any(), chunkMarkerKey(streamUUID, 0)).
		Return(int64(len(doneMarker)), nil)
	stor.EXPECT().
		Download(gomock.Any(), chunkMarkerKey(streamUUID, 0), gomock.Any()).
		DoAndReturn(func(ctx context.Context, key, local string) error {
			return os.WriteFile(local, doneMarker, 0o644)
		})
	stor.EXPECT().StatSize(gomock.Any(), chunkMarkerKey(streamUUID, 1)).
		Return(int64(0), fmt.Errorf("not found"))

	// Only chunk 1 runs.
	proc.EXPECT().
		TranscodeChunk(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, in, out string, start, dur float64) (<-chan processor.Progress, <-chan error) {
			if start != 1800 {
				t.Errorf("resumed task re-encoded chunk 0 (start=%.0f)", start)
			}
			writeTestChunkOutput(t, out)
			return closedProgressChans()
		}).
		Times(1)
	stor.EXPECT().
		UploadDir(gomock.Any(), fmt.Sprintf("processed/%s/chunks/c1", streamUUID), gomock.Any()).
		Return(nil)

	var gotIndex string
	markerUploads := 0
	stor.EXPECT().
		Upload(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, key, local, contentType string) error {
			if strings.HasSuffix(key, "index.m3u8") {
				data, err := os.ReadFile(local)
				if err != nil {
					return err
				}
				gotIndex = string(data)
				return nil
			}
			markerUploads++
			return nil
		}).
		Times(2)

	require.NoError(t, handler.HandleVideoTranscoderTask(context.Background(), task))

	require.Equal(t, 1, markerUploads, "only chunk 1 writes a new marker")
	assert.Contains(t, gotIndex, "chunks/c0/seg_0.ts", "chunk 0 segments come from the marker")
	assert.Contains(t, gotIndex, "chunks/c1/seg_0.ts")
}

func TestHandle_ChunkedWatchdogStallsSilentFFmpeg(t *testing.T) {
	old := chunkWatchdog
	chunkWatchdog = 50 * time.Millisecond
	t.Cleanup(func() { chunkWatchdog = old })

	ctrl := gomock.NewController(t)
	streamUUID := uuid.New()
	payload := VideoTranscodingPayload{StreamUUID: streamUUID, InputPath: "raw/video.mp4"}
	task := asynq.NewTask(TaskVideoTranscoding, mustMarshalPayload(t, payload))
	proc, stor, _, handler := chunkedTestEnv(t, ctrl, streamUUID, payload.InputPath, 3601)

	stor.EXPECT().StatSize(gomock.Any(), chunkMarkerKey(streamUUID, 0)).
		Return(int64(0), fmt.Errorf("not found"))

	// ffmpeg never emits a single event: the watchdog must cancel the chunk
	// instead of letting the task sit until the asynq timeout.
	proc.EXPECT().
		TranscodeChunk(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, in, out string, start, dur float64) (<-chan processor.Progress, <-chan error) {
			return make(chan processor.Progress), make(chan error)
		}).
		Times(1)

	err := handler.HandleVideoTranscoderTask(context.Background(), task)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stalled")
}

func TestHandle_ChunkedFailureKeepsEarlierCheckpoints(t *testing.T) {
	ctrl := gomock.NewController(t)
	streamUUID := uuid.New()
	payload := VideoTranscodingPayload{StreamUUID: streamUUID, InputPath: "raw/video.mp4"}
	task := asynq.NewTask(TaskVideoTranscoding, mustMarshalPayload(t, payload))
	proc, stor, _, handler := chunkedTestEnv(t, ctrl, streamUUID, payload.InputPath, 3600)

	stor.EXPECT().StatSize(gomock.Any(), chunkMarkerKey(streamUUID, 0)).
		Return(int64(0), fmt.Errorf("not found"))
	stor.EXPECT().StatSize(gomock.Any(), chunkMarkerKey(streamUUID, 1)).
		Return(int64(0), fmt.Errorf("not found"))

	call := 0
	proc.EXPECT().
		TranscodeChunk(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, in, out string, start, dur float64) (<-chan processor.Progress, <-chan error) {
			call++
			if call == 1 {
				writeTestChunkOutput(t, out)
				return closedProgressChans()
			}
			prog := make(chan processor.Progress)
			errs := make(chan error, 1)
			errs <- fmt.Errorf("processing error")
			close(prog)
			close(errs)
			return prog, errs
		}).
		Times(2)

	// Chunk 0 fully lands (segments + marker); chunk 1 fails before its
	// upload, so exactly one marker and one UploadDir happen in total.
	stor.EXPECT().
		UploadDir(gomock.Any(), fmt.Sprintf("processed/%s/chunks/c0", streamUUID), gomock.Any()).
		Return(nil)

	markerUploads := 0
	stor.EXPECT().
		Upload(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, key, local, contentType string) error {
			if strings.HasSuffix(key, "index.m3u8") {
				return fmt.Errorf("index must not be uploaded after a failed chunk")
			}
			markerUploads++
			return nil
		}).
		Times(1)

	err := handler.HandleVideoTranscoderTask(context.Background(), task)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "processing error")
	assert.Equal(t, 1, markerUploads, "the completed chunk keeps its checkpoint for the retry")
}

// memStorage is an in-memory FileStorage for the end-to-end chunked test:
// it lets a real ffmpeg result travel through the upload, stitch and resume
// machinery without a live MinIO.
type memStorage struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func newMemStorage() *memStorage {
	return &memStorage{blobs: make(map[string][]byte)}
}

func (m *memStorage) set(key string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[key] = data
}

func (m *memStorage) get(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.blobs[key]
	return data, ok
}

func (m *memStorage) keysWithPrefix(prefix string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.blobs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys
}

func (m *memStorage) Download(ctx context.Context, remoteKey, localPath string) error {
	data, ok := m.get(remoteKey)
	if !ok {
		return fmt.Errorf("object does not exist: %s", remoteKey)
	}
	return os.WriteFile(localPath, data, 0o644)
}

func (m *memStorage) GeneratePresignedURL(ctx context.Context, remoteKey string, expires time.Duration) (string, error) {
	return "http://minio.test/bucket/" + remoteKey + "?sig=test", nil
}

func (m *memStorage) StatSize(ctx context.Context, remoteKey string) (int64, error) {
	data, ok := m.get(remoteKey)
	if !ok {
		return 0, fmt.Errorf("object does not exist: %s", remoteKey)
	}
	return int64(len(data)), nil
}

func (m *memStorage) DownloadDir(ctx context.Context, remoteDir, localDir string) error {
	prefix := remoteDir + "/"
	keys := m.keysWithPrefix(prefix)
	if len(keys) == 0 {
		return fmt.Errorf("object does not exist: %s", remoteDir)
	}
	for _, k := range keys {
		rel := strings.TrimPrefix(k, prefix)
		dst := filepath.Join(localDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := m.Download(ctx, k, dst); err != nil {
			return err
		}
	}
	return nil
}

func (m *memStorage) Upload(ctx context.Context, remoteKey, localPath, contentType string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	m.set(remoteKey, data)
	return nil
}

func (m *memStorage) UploadDir(ctx context.Context, remoteDir, localDir string) error {
	return filepath.WalkDir(localDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(localDir, path)
		if err != nil {
			return err
		}
		return m.Upload(ctx, remoteDir+"/"+rel, path, "application/octet-stream")
	})
}

// TestHandle_ChunkedTranscodeEndToEnd is the pipeline contract in one test:
// a real 12s source split by an 8s threshold goes through probe, chunked
// encode, per-chunk uploads, checkpoint markers and the stitched index —
// and the stitched rendition must then survive a real ffmpeg mux (which is
// also what the mp4 export worker does with it).
func TestHandle_ChunkedTranscodeEndToEnd(t *testing.T) {
	ffproc, err := processor.NewFFmpegProcessor(processor.EncoderCPU)
	if err != nil {
		t.Skipf("ffmpeg not installed: %v", err)
	}
	old := chunkDurationSec
	chunkDurationSec = 8
	t.Cleanup(func() { chunkDurationSec = old })

	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	encode := exec.Command("ffmpeg",
		"-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=15:duration=12",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=12",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "15",
		"-c:a", "aac", "-b:a", "64k",
		"-shortest", src)
	if out, err := encode.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v\n%s", err, out)
	}
	srcData, err := os.ReadFile(src)
	require.NoError(t, err)

	mem := newMemStorage()
	mem.set("raw/video.mp4", srcData)

	ctrl := gomock.NewController(t)
	svc := mockSvc.NewMockStreamServiceClient(ctrl)
	svc.EXPECT().UpdateStreamMetadata(gomock.Any(), gomock.Any()).
		Return(&stream.UpdateStreamMetadataResponse{}, nil).AnyTimes()
	svc.EXPECT().UpdateStreamProcessing(gomock.Any(), gomock.Any()).
		Return(&stream.UpdateStreamProcessingResponse{Updated: true}, nil).AnyTimes()
	svc.EXPECT().UpdateStreamStatus(gomock.Any(), gomock.Any()).
		Return(&stream.UpdateStreamStatusResponse{}, nil).AnyTimes()

	handler := NewHandleVideoTranscoder(ffproc, mem, svc)
	streamUUID := uuid.New()
	payload := VideoTranscodingPayload{StreamUUID: streamUUID, InputPath: "raw/video.mp4"}
	task := asynq.NewTask(TaskVideoTranscoding, mustMarshalPayload(t, payload))

	// Presign points at a dead host, so the handler falls back to the local
	// download — the same path a MinIO outage would take.
	require.NoError(t, handler.HandleVideoTranscoderTask(context.Background(), task))

	remoteRoot := "processed/" + streamUUID.String()
	for i := 0; i < 2; i++ {
		_, ok := mem.get(fmt.Sprintf("%s/chunks/done_%d.json", remoteRoot, i))
		require.True(t, ok, "chunk %d marker missing", i)
		segments := mem.keysWithPrefix(fmt.Sprintf("%s/chunks/c%d/", remoteRoot, i))
		require.NotEmpty(t, segments, "chunk %d segments missing", i)
		for _, k := range segments {
			assert.NotContains(t, k, "index.m3u8", "chunk playlist must not be uploaded")
		}
	}

	index, ok := mem.get(remoteRoot + "/index.m3u8")
	require.True(t, ok, "stitched index missing")
	idx := string(index)
	assert.Contains(t, idx, "chunks/c0/")
	assert.Contains(t, idx, "chunks/c1/")
	assert.Equal(t, 1, strings.Count(idx, "#EXT-X-DISCONTINUITY"))
	assert.Contains(t, idx, "#EXT-X-ENDLIST")

	// Pull the whole rendition back and hand it to the same mux the export
	// worker runs: a broken boundary or a missing segment fails here.
	local := t.TempDir()
	require.NoError(t, mem.DownloadDir(context.Background(), remoteRoot, local))
	out := filepath.Join(local, "out.mp4")
	require.NoError(t, ffproc.MuxToMP4(context.Background(), filepath.Join(local, "index.m3u8"), out))

	stat, err := os.Stat(out)
	require.NoError(t, err)
	assert.Greater(t, stat.Size(), int64(0))

	gotDur, err := ffproc.GetDuration(context.Background(), out)
	require.NoError(t, err)
	assert.InDelta(t, 12, gotDur, 1.0, "stitched rendition must cover the whole source")
}

func mustMarshalPayload(t *testing.T, p VideoTranscodingPayload) []byte {
	t.Helper()
	data, err := json.Marshal(p)
	require.NoError(t, err)
	return data
}
