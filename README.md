# transcoder-service

Transcodes uploaded videos into HLS for GoCast. An **asynq** worker scaled by **KEDA** —
it sleeps at 0 replicas and wakes up when a transcoding task lands in the queue.

## How it works

- Stream-service enqueues a transcoding task (Redis asynq queue, DB 2, `default` queue key);
- KEDA `ScaledObject` watches `asynq:{default}:pending` / `asynq:{default}:active` (list/zset)
  and scales the deployment from 0 to 1;
- The worker reads the source from MinIO, runs ffmpeg (`internal/processor`), writes HLS
  segments + `.m3u8` into MinIO under `processed/<streamUUID>/`, and reports progress via
  gRPC `UpdateStreamProcessing` back to stream-service (including error state on failure);
- KEDA scales back to 0 when the queue drains (normal).

## Spec

| Layer | Tech |
|---|---|
| Task queue | Asynq (Redis), `default` queue |
| Processing | ffmpeg (`internal/processor`) |
| Storage | MinIO (`internal/storage`) |
| gRPC | Client to stream-service (**mTLS**, serverName `stream-service`) |
| Metrics | Prometheus `/metrics` on `METRICS_ADDR` (default `:9090` in K8s) |
| Config | `sharedconfig` from `go-shared` (env-driven) |

## Metrics

Exposed via `METRICS_ADDR` (empty = off) with `promhttp`:

- `transcoder_processed_total`, `transcoder_processing_duration_seconds`,
  `transcoder_processing_errors_total`, `transcoder_disk_full_total`, `transcoder_aborted_total`

Plus shared asynq-task metrics from `go-shared/metrics`. Kubernetes liveness probes hit
`/metrics` (HTTP GET) instead of `ps`, and the Deployment carries `prometheus.io/*` annotations.

## Configuration

Loaded from env by `sharedconfig.LoadConfig()` (root `.env` → `transcoder-service-config`
ConfigMap). See `services/shared/README.md` for the full variable list; the relevant ones:
`REDIS_ADDR`, `MINIO_ENDPOINT`/`MINIO_ACCESS_KEY`/`MINIO_SECRET_KEY`/`MINIO_BUCKET_NAME`,
`STREAM_SERVICE_ADDRESS`, `GRPC_TLS_*`, `METRICS_ADDR`.

## Deployment

K8s manifests under `deploy/k8s/`:

```
deploy/k8s/
├── keda/
│   ├── auth.yaml           # Redis trigger auth
│   └── scaledobject.yaml   # scale from/to 0 based on queue length (default queue, DB 2)
└── transcoder/
    ├── deployment.yaml     # image xomrkob/transcoder-service:<git-tag>, metrics containerPort
    └── service.yaml        # ClusterIP for :9090 metrics scraping
```

Build/push/deploy: `make build push deploy` (image `xomrkob/transcoder-service:<git-tag>`).

The asynq worker scaffolding (`worker.NewAsynqServer`, ErrorHandler wiring) is shared with
thumbnail — see `services/shared/worker`.