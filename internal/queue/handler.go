package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/hibiken/asynq"
)

func HandleVideoTranscodeTask(ctx context.Context, t *asynq.Task) error {
	var p VideoTranscodingPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("json unmarshal failed: %v", err)
	}
	slog.Info("[Worker] Start processing video:", "Stream UUID", p.StreamUUID)
	// Video HandleVideoTranscodeTask(
	//
	// )
	slog.Info("[Worker] Finish processing video:", "Stream UUID", p.StreamUUID)
	return nil
}
