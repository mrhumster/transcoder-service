package queue

import "github.com/google/uuid"

const TaskVideoTranscoding = "video:transcode"

type VideoTranscodingPayload struct {
	StreamUUID uuid.UUID `json:"stream_uuid"`
	InputPath  string    `json:"input_path"`
}
