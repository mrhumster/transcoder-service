package queue

import "github.com/google/uuid"

const (
	TaskVideoTranscoding = "video:transcode"
	TaskVideoExport      = "video:export"
)

type VideoTranscodingPayload struct {
	StreamUUID uuid.UUID `json:"stream_uuid"`
	InputPath  string    `json:"input_path"`
}

// VideoExportPayload asks for a single-file rendition of a stream that has
// already been transcoded. OwnerEmail is carried along so the ready
// notification can be enqueued without a user lookup.
type VideoExportPayload struct {
	StreamUUID uuid.UUID `json:"stream_uuid"`
	OwnerUUID  uuid.UUID `json:"owner_uuid"`
	OwnerEmail string    `json:"owner_email"`
}
