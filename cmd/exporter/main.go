// Command exporter builds the cached single-file rendition of a stream on
// demand. It lives in the same image as the transcoder worker but consumes a
// separate queue on a separate Redis DB, so exporting a long video can never
// queue behind, or starve, a transcode.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/hibiken/asynq"
	sharedconfig "github.com/mrhumster/go-shared/config"
	sharedgrpctls "github.com/mrhumster/go-shared/grpctls"
	sharedmetrics "github.com/mrhumster/go-shared/metrics"
	sharedworker "github.com/mrhumster/go-shared/worker"
	pb "github.com/mrhumster/transcoder-service/gen/go/stream"
	"github.com/mrhumster/transcoder-service/internal/processor"
	"github.com/mrhumster/transcoder-service/internal/queue"
	"github.com/mrhumster/transcoder-service/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	version   = "dev"
	buildDate = "unknown"
)

// emailDownloadReadyTaskType is handled by mailer-service. It is duplicated
// here on purpose: transcoder-service must not depend on the mailer's Go module
// just to name a string.
const emailDownloadReadyTaskType = "email:download_ready"

func main() {
	opts := &slog.HandlerOptions{Level: slog.LevelDebug, AddSource: true}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, opts)))

	cfg, err := sharedconfig.LoadConfig()
	if err != nil {
		slog.Error("error load config", "error", err)
		os.Exit(1)
	}

	minioStorage, err := storage.NewMinIOStorageFromConfig(cfg.MinIO)
	if err != nil {
		slog.Error("error init minio storage", "error", err)
		os.Exit(1)
	}

	ffmpeg, err := processor.NewFFmpegProcessor(cfg.Transcoder.Encoder)
	if err != nil {
		slog.Error("error init processor", "error", err)
		os.Exit(1)
	}

	creds := insecure.NewCredentials()
	if cfg.Server.GRPCTLSEnabled {
		creds, err = sharedgrpctls.ClientTLSCreds(
			cfg.Server.GRPCTLSCertFile, cfg.Server.GRPCTLSKeyFile, cfg.Server.GRPCTLSCAFile, "stream-service")
		if err != nil {
			slog.Error("error init gRPC TLS client", "error", err)
			os.Exit(1)
		}
	}

	conn, err := grpc.NewClient(cfg.Server.StreamServiceAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		slog.Error("error init gRPC client", "error", err)
		os.Exit(1)
	}
	defer conn.Close()

	streamServiceClient := pb.NewStreamServiceClient(conn)
	notify, err := newMailNotifier(cfg)
	if err != nil {
		slog.Error("error init mail notifier", "error", err)
		os.Exit(1)
	}

	// If the process dies mid-task, asynq hands the error here; without this the
	// row would stay pending forever and the UI would wait for an email that
	// never comes.
	reportFailure := func(ctx context.Context, task *asynq.Task, err error) {
		var p queue.VideoExportPayload
		if uerr := json.Unmarshal(task.Payload(), &p); uerr != nil || p.StreamUUID.String() == "" {
			return
		}
		if _, rerr := streamServiceClient.CompleteStreamExport(ctx, &pb.CompleteStreamExportRequest{
			StreamUuid: p.StreamUUID.String(),
			Success:    false,
			Error:      fmt.Sprintf("worker lost the task: %v", err),
		}); rerr != nil {
			slog.Error("failed to report export failure", "stream", p.StreamUUID, "error", rerr)
		}
	}

	srv, err := sharedworker.NewAsynqServer(sharedworker.Options{
		Addr:            cfg.Redis.Addr,
		Password:        cfg.Redis.Password,
		DB:              cfg.Redis.DB,
		Concurrency:     cfg.Worker.Concurrency,
		ShutdownTimeout: cfg.Worker.ShutdownTimeout,
		ErrorReporter:   reportFailure,
		MetricsAddr:     cfg.Server.MetricsAddr,
	})
	if err != nil {
		slog.Error("error init asynq worker", "error", err)
		os.Exit(1)
	}

	handler := queue.NewHandleVideoExport(ffmpeg, minioStorage, streamServiceClient, notify)
	mux := asynq.NewServeMux()
	mux.HandleFunc(queue.TaskVideoExport, sharedmetrics.Instrument(queue.TaskVideoExport, handler.HandleVideoExportTask))

	slog.Info("Exporter worker started...",
		"version", version,
		"queue", queue.TaskVideoExport,
		"redis_db", cfg.Redis.DB,
	)
	if err := srv.Run(mux); err != nil {
		slog.Error("could not run asynq server", "error", err)
		os.Exit(1)
	}
}

// newMailNotifier returns a function that asks mailer-service to send the
// "your download is ready" message, or nil when no SMTP configuration is
// present. A missing mailer must not stop exports: the file is downloadable
// from the web either way.
func newMailNotifier(cfg *sharedconfig.Config) (func(context.Context, queue.VideoExportPayload, int64) error, error) {
	if cfg.Mail.SenderAddr == "" || cfg.Mail.FrontendURL == "" {
		slog.Warn("mail notification disabled: SMTP_ADDR or FRONTEND_URL is unset", "smtp", cfg.Mail.SenderAddr != "", "frontend_url", cfg.Mail.FrontendURL != "")
		return nil, nil
	}

	mailerDB, err := envInt("MAILER_REDIS_DB", 2)
	if err != nil {
		return nil, err
	}
	client := asynq.NewClient(asynq.RedisClientOpt{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       mailerDB,
	})

	return func(ctx context.Context, p queue.VideoExportPayload, size int64) error {
		if p.OwnerEmail == "" {
			// The access token carried no email claim (or it was unverified);
			// the export still succeeded and the user can download from the page.
			slog.Warn("no owner email in the export payload, skipping notification", "stream", p.StreamUUID)
			return nil
		}
		payload, err := json.Marshal(map[string]any{
			"user_id":    p.OwnerUUID.String(),
			"email":      p.OwnerEmail,
			"stream_id":  p.StreamUUID.String(),
			"stream_url": cfg.Mail.FrontendURL + "/streams/" + p.StreamUUID.String(),
			"size":       size,
		})
		if err != nil {
			return err
		}
		_, err = client.EnqueueContext(ctx,
			asynq.NewTask(emailDownloadReadyTaskType, payload),
			asynq.Queue("emails"),
			asynq.MaxRetry(3),
		)
		return err
	}, nil
}

func envInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", key, err)
	}
	return v, nil
}
