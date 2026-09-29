# transcoder-service

Transcodes uploaded videos into HLS for GoCast. An **asynq** worker scaled by **KEDA** —
it sleeps at 0 replicas and wakes up when a transcoding task lands in the queue.

## How it works

- stream-service enqueues a `video:transcode` task onto the asynq `default` queue
  (Redis DB 2);
- KEDA `ScaledObject` watches `asynq:{default}:pending` / `asynq:{default}:active` and scales
  the deployment from 0 to 1;
- the handler (`internal/queue/handler.go`) downloads the source from MinIO into `/tmp`,
  probes it with ffprobe, runs ffmpeg (`internal/processor`) into a local HLS directory,
  then uploads `index.m3u8` + segments back under `processed/<streamUUID>/`;
- progress is reported to stream-service over gRPC `UpdateStreamProcessing` (0 → 100, throttled by
  5 points, and a final `100` is always sent — even when ffmpeg fails), the extracted metadata via
  `UpdateStreamMetadata`, and the final state via `UpdateStreamStatus`;
- KEDA scales back to 0 when the queue drains (normal).

### Task contract

| Field | Value |
|---|---|
| Task type | `video:transcode` (`queue.TaskVideoTranscoding`) |
| Payload | `{"stream_uuid": "<uuid>", "input_path": "<minio key>"}` |
| Handler | `HandleVideoTranscoderTask` (method) on `*HandleVideoTrancoder` (misspelled type), built by `NewHandleVideoTranscoder` |

The server is started with `sharedworker.Options` and **no `Queues` option**, so asynq's default
queue set is used — the worker consumes everything that lands in `default`, with no per-task-type
filtering of its own.

Only the transcode handler is wrapped in `sharedmetrics.Instrument`
(`mux.HandleFunc(queue.TaskVideoTranscoding, …)`), so `asynq_task_*` reflects `video:transcode`
work alone. No other shared task types are registered or instrumented here.

### Handler flow (`internal/queue/handler.go`)

1. `os.Mkdir` `/tmp/<stream-uuid>` and `/tmp/<stream-uuid>/hls`; a deferred `os.RemoveAll` removes the
   work dir whatever happens;
2. `storage.Download` the source to `input.mp4`. A missing object and a download-time
   `no space left on device` return `asynq.SkipRetry` (the latter bumps `transcoder_disk_full_total`);
   **any other download error returns `nil`** (see Known issues);
3. `ProbeMetadata` the local copy (failure is logged, not fatal) and `UpdateStreamMetadata`
   (recorded_at / location / camera / size / duration) — **before** transcoding, with
   `Format: "hls"` and `Resolution: "1280x720"` hardcoded (the actual output resolution is never
   probed); a failure here is returned and retried;
4. `TranscodeToHLS` with throttled `UpdateStreamProcessing` progress parsed from ffmpeg's
   `-progress pipe:1`; every progress tick also runs the `/tmp` guard (below);
5. `storage.UploadDir` the HLS output to `processed/<stream-uuid>`;
6. `UpdateStreamStatus` to `ready`, final `UpdateStreamProcessing` 100%.

Cancellation via context marks the task aborted (`transcoder_aborted_total`).

**Progress reporting.** `lastSentPercent` starts at `-1` and updates are sent when the reported
percent reaches `lastSentPercent + 5` (or 100), so there is **no guaranteed initial `0%`** — a
very short source can jump straight to 100. Two consequences worth knowing:

- `TranscodeToHLS` emits `Progress{Percent: 100, Finished: true}` *before* `cmd.Wait()`, so a
  failing encode can still have reported 100% just before the error;
- an error update reuses `lastSentPercent`, so the failure can surface as `-1` (if no progress
  event ever arrived) or as `100` (if the forced completion event already landed).

`steps` only ever contains `"Transcoding"` (progress / ffmpeg error / disk guard),
`"Uploading to the storage"` (sent *before* the upload walk, at 100%) or an empty list (the final
100% after `ready`). The single `"Processing"` step comes from a different place — the asynq
`ErrorReporter` in `cmd/worker/main.go`, which reports `Progress: 0` with
`"Worker died or resourse limit exceeded"` when the task is finally abandoned.

**`/tmp` guard.** On every progress event the handler measures the *cumulative* size of `/tmp`
(`getDirSize("/tmp")`, i.e. total bytes in use, not free space) and, above
`9 * 1024 * 1024 * 1024`, sends an error update (`Progress: 0`, `steps: ["Transcoding"]`),
increments `transcoder_disk_full_total` and returns `asynq.SkipRetry`. Two caveats: `getDirSize`
returns `0` on traversal errors, and the measurement covers everything else sharing `/tmp` in the
pod, so the guard can trip for reasons unrelated to the current transcode.

### ffmpeg invocation

`internal/processor/ffmpeg.go` builds the argument list:

| Setting | Value |
|---|---|
| Video bitrate | `-b:v 2500k -maxrate 2500k -bufsize 5000k` |
| Audio | `-c:a aac` |
| HLS segment | `-hls_time 10 -hls_list_size 0` (one playlist, no rolling window) |
| Segment name | `seg_%d.ts` |
| CPU encoder | `-threads 0 -c:v libx264` |
| VAAPI encoder | `-vaapi_device /dev/dri/renderD128` (before `-i`), `-vf format=nv12,hwupload`, `-c:v h264_vaapi` (no `-threads`) |

## Spec

| Layer | Tech |
|---|---|
| Task queue | Asynq (Redis), `default` queue, DB 2 |
| Processing | ffmpeg / ffprobe (`internal/processor`) |
| Storage | MinIO (`internal/storage`) |
| gRPC | Client to stream-service (**mTLS**, serverName `stream-service`) |
| Metrics | Prometheus `/metrics` on `METRICS_ADDR` (`:9090` in K8s, empty = off) |
| Config | `sharedconfig` from `go-shared` (env-driven) |
| Module | `github.com/mrhumster/transcoder-service`, Go 1.25.14 |

## Metrics

Exposed on `METRICS_ADDR` (empty string disables the server) via `promhttp`:

| Metric | Type | Meaning |
|---|---|---|
| `transcoder_processed_total` | counter | Tasks that returned `nil` — **including** the generic download-error path that returns `nil` by mistake |
| `transcoder_errors_total` | counter | Tasks that returned a non-nil error |
| `transcoder_disk_full_total` | counter | Download-time `no space left on device` errors **and** cumulative-`/tmp` guard trips; ffmpeg-reported disk-full errors are *not* counted |
| `transcoder_aborted_total` | counter | Context cancellation only |
| `transcoder_duration_seconds` | histogram | Wall time, observed only for tasks that returned `nil` |

Plus the shared asynq metrics from `go-shared/metrics` (`asynq_task_processed_total`,
`asynq_task_duration_seconds`, `asynq_task_inflight`) and Go runtime metrics. Those vectors are
created lazily, so the `asynq_task_*` series appear only after the first task runs — and because
the buggy download path returns `nil`, it is also labelled `success` there.

The Deployment carries `prometheus.io/scrape|port|path` annotations and the liveness probe is an
HTTP GET on `/metrics` (not `ps`), so a running metrics server doubles as the health signal.
Because the transcoder counters are registered in `init()` they appear as `0` immediately; the
histogram series exist as soon as the server starts.

## Configuration

Loaded by `sharedconfig.LoadConfig()`. In Kubernetes the values come from
`transcoder-service-config` (generated by `scripts/render-env.sh` from the root `.env`),
`go-app-config`, the `casbin-redis` and `minio-credentials` secrets, plus inline `GRPC_TLS_*`.
See `services/shared/README.md` for the loader.

| Variable | Default | Notes |
|---|---|---|
| `REDIS_ADDR` | `localhost` | asynq/Redis host |
| `REDIS_DB` | `2` | queue database |
| `redis-password` | — | Redis password; the env var name is literally the secret key name |
| `MINIO_ENDPOINT` | `localhost:9000` | |
| `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` | `admin` / `minio123` | dev defaults only |
| `MINIO_BUCKET_NAME` | `stream-service-test` | |
| `MINIO_USE_SSL` / `MINIO_REGION` | `false` / `ru-east-1` | |
| `STREAM_SERVICE_ADDR` | `localhost:50051` | gRPC target for progress reporting |
| `GRPC_TLS_ENABLED` | `false` | set to `true` in K8s |
| `GRPC_TLS_CERT` / `GRPC_TLS_KEY` / `GRPC_TLS_CA` | — | mounted from secret `grpc-transcoder-tls` at `/tls/grpc` |
| `METRICS_ADDR` | `""` | `:9090` in the generated ConfigMap |
| `WORKER_CONCURRENCY` | `1` | |
| `WORKER_SHUTDOWN_TIMEOUT` | `50m` | |
| `WORKER_RETRY_DELAY` | `30s` | parsed by the config loader but **not** wired into the asynq server — the asynq default backoff applies |
| `TRANSCODER_ENCODER` | `auto` | `auto` \| `cpu` \| `vaapi` |

## gRPC / mTLS

The worker only **calls** stream-service (no server). `cmd/worker/main.go` builds the client with
`grpctls.ClientTLSCreds(cert, key, ca, "stream-service")` when `GRPC_TLS_ENABLED=true`; the peer
certificate must be issued for `stream-service` by the same local CA as the other services.
The client cert must carry the `transcoder-service` OU — stream-service's `AllowOUsInterceptor`
accepts `transcoder-service` and `thumbnail-service`.

## GPU encoding (VAAPI)

`TRANSCODER_ENCODER` selects the encoder at startup in `NewFFmpegProcessor`:

| Value | Effective encoder | Behaviour |
|---|---|---|
| `auto` (default) | `h264_vaapi` if available, else `libx264` | probes `/dev/dri/renderD128` and `ffmpeg -encoders` |
| `cpu` | `libx264` | always software |
| `vaapi` | `h264_vaapi` if available, else `libx264` | forced, but **degrades to CPU instead of failing** |
| anything else | `libx264` | unknown value warns and falls back |

Capability detection is `os.Stat("/dev/dri/renderD128")` plus a grep of `h264_vaapi` in
`ffmpeg -encoders`. Failures during resolution are logged as warnings only — the worker always
starts with a usable encoder, so a misconfigured `TRANSCODER_ENCODER` silently produces
software-encoded output rather than an error.

Built for **AMD RX 5700 XT** (RDNA/Navi 10) — no NVENC/CUDA needed. The runtime image installs
`libva` + `mesa-dri-gallium` and symlinks
`/usr/lib/dri/radeonsi_dri.so → ../xorg/modules/dri/radeonsi_dri.so` because Alpine ships DRI
drivers under the xorg modules directory. `h264_vaapi` is part of the Alpine 3.18 ffmpeg package,
so no source build is required. macOS/WSL dev hosts without `/dev/dri` just run `cpu`.

## Video metadata extraction (ffprobe)

Before transcoding, the worker probes the **original** file and sends the result in the gRPC
`UpdateStreamMetadata` call.

`internal/processor/metadata.go` — `ProbeMetadata` runs
`ffprobe -v error -print_format json -show_format -show_streams` and `metadataFromProbe`
maps the JSON onto `VideoMetadata{RecordedAt, Location, Camera, Duration, Size}`:

| Field | Source | Notes |
|---|---|---|
| `recorded_at` | `creation_time` / `com.apple.quicktime.creationdate` | `parseRecordedAt` handles RFC3339Nano, `UTC`/`UTC9` MSF, RFC1123, `YYYYMMDD HHMMSS` |
| `location` | `com.apple.quicktime.location.ISO6709` | ISO 6709 → `"lat,lng"` (decimal and DDDMMSS forms); plain `location` tags are parsed too and only stored raw if they are not ISO 6709 |
| `camera` | `make` + `model` | merged into `"Make Model"` |
| `size` | `os.Stat(inputLocal)` | real file size, no longer zeroed after transcode |

**Tag placement:** format-level tags (`-show_format`) are preferred, with a fallback to the first
media stream's tags. This covers QuickTime containers that write into the `udta` atom (format
level) and files that only carry the tags in an `mdta` stream atom (muxed with
`-movflags use_metadata_tags`, where the keys surface under the stream). A `mergeTags` /
`firstNonEmpty` helper keeps the lookup order consistent. ffmpeg 6.1.1 itself only writes
`creation_time` on a plain remux — the QuickTime `location`/`make`/`model` tags end up on the
stream level, which is why the fallback matters for real camera files (iPhone/GoPro).

Values are converted to protobuf: `RecordedAtString` emits UTC RFC3339Nano, or an empty string
when the source had no creation time (stream-service tolerates and ignores empty values).

## Project layout

```
transcoder-service/
├── cmd/worker/main.go            # asynq server wiring, gRPC/mTLS client, metrics server
├── internal/
│   ├── metrics/metrics.go        # business counters + histogram
│   ├── processor/ffmpeg.go       # encoder resolution, buildArgs, TranscodeToHLS
│   ├── processor/metadata.go     # ffprobe → VideoMetadata
│   ├── processor/processor.go    # VideoProcessor interfaces
│   ├── processor/mock/           # gomock VideoProcessor
│   ├── queue/payload.go          # task type + payload
│   ├── queue/handler.go          # disk check, download, progress, upload, gRPC updates
│   ├── service/mock/             # gomock StreamServiceClient
│   ├── storage/                  # MinIO FileStorage (Download / UploadDir)
│   └── storage/mock/             # gomock FileStorage, MinioClient
├── gen/go/stream/                # generated gRPC code (from ../proto)
├── proto/stream/                 # copy of the shared stream_service.proto
├── scripts/test-local-cover.sh   # go test + coverage HTML
└── Dockerfile                    # alpine:3.18 + ffmpeg/libva, non-root appuser (uid 1000)
```

The build context is the **parent `services/` directory** (`docker build -f Dockerfile ..`), because
the Dockerfile copies both `transcoder-service/` and the shared `shared/` module.

## Commands

```bash
make build        # docker build with VERSION from git describe, tags VERSION + latest
make push         # docker push both tags
make deploy       # kubectl set image deployment/transcoder-service (no rollout wait)
make test         # go test -v ./...
make proto        # regenerate gen/go from proto/stream (needs protoc)
make keda-deploy  # helm install KEDA into namespace keda
make logs         # kubectl logs -f -l app=transcoder
```

`make deploy` runs `kubectl set image` but does not wait for the rollout — add
`kubectl -n go-app rollout status deployment/transcoder-service` yourself. Because the tag comes
from `git describe`, rebuilding an unchanged working tree reuses the tag and `set image` becomes
a no-op; use `kubectl rollout restart deployment/transcoder-service -n go-app` in that case.

## Deployment

```
deploy/k8s/
├── keda/
│   ├── auth.yaml           # Redis trigger auth (secret casbin-redis, key redis-password)
│   └── scaledobject.yaml   # scale 0..1 on default-queue length
└── transcoder/
    ├── deployment.yaml     # image xomrkob/transcoder-service:<tag>, metrics :9090
    └── service.yaml        # ClusterIP for :9090 metrics scraping
```

KEDA polls Redis DB 2 lists `asynq:{default}:pending` and `asynq:{default}:active`
(the latter declared as a zset) and scales when either exceeds `listLength: 2`;
`minReplicaCount: 0`, `maxReplicaCount: 1`, `cooldownPeriod: 120`. Both manifests live in git but
`make keda-deploy` only installs the KEDA chart — the `ScaledObject` and the `keda-redis-auth`
`TriggerAuthentication` have to be applied by hand. Note that the transcoder shares the `default`
queue with other asynq producers, so a busy default queue will also wake this worker.

The Deployment is GPU-ready out of the box:

- `hostPath /dev/dri` (DirectoryOrCreate) mounted at `/dev/dri` so VAAPI can reach the render node;
- `securityContext.runAsUser: 0` — **overrides the image's `appuser` (uid 1000)**; required for
  `/dev/dri` access on this homelab setup (a device plugin would be cleaner);
- `terminationGracePeriodSeconds: 300` + `preStop: sleep 5` (down from 3600) — this makes rolling
  updates terminate, but it **caps** a single transcode at roughly five minutes, so a longer encode
  is SIGKILLed mid-run. The task stays invisible until its asynq lease/visibility timeout expires,
  then another attempt (or a manual Reprocess) is needed; the retry re-downloads and re-transcodes
  from scratch;
- 10Gi `emptyDir` at `/tmp` is the scratch space for source + HLS output;
- resources: requests `250m` CPU / `512Mi`, limits `2000m` / `1Gi` (tuned for a 4-core node);
- `nodeSelector`/`tolerations` for a `gpu=true` node are present but **commented out** — enable
  them once a GPU-capable k3s agent (`--node-label gpu=true`) joins.

The asynq worker scaffolding (`worker.NewAsynqServer`, ErrorHandler wiring) is shared with
thumbnail — see `services/shared/worker`.

## Known issues

- **Wrong metric names in older docs.** The authoritative names are `transcoder_duration_seconds`
  and `transcoder_errors_total`; `transcoder_processing_duration_seconds` /
  `transcoder_processing_errors_total` do not exist and will never appear in a scrape.
- **A generic download failure is reported as success.** `internal/queue/handler.go` returns `nil`
  for any `storage.Download` error that is neither `does not exist` nor
  `no space left on device`, so the task is marked successful (counted in
  `transcoder_processed_total` and as `success` in `asynq_task_processed_total`), the stream never
  reaches `ready`, and no error update is sent — the stream just sits in `processing` until someone
  notices. Only those two error strings are classified; anything else (auth, timeout, network,
  `context deadline exceeded`) falls into this hole.
- **Retries repeat the whole pipeline.** stream-service enqueues `video:transcode` with
  `asynq.MaxRetry(1)`, and the handler is not resumable, so a failure in `UpdateStreamMetadata`,
  `UploadDir` or `UpdateStreamStatus` re-downloads the source, re-runs ffmpeg and re-uploads the
  whole HLS directory on the single retry. Failures that are actually deterministic (bad input,
  missing source) are excluded only when the error text matches, see above.
- **An early handler return kills ffmpeg abruptly.** `TranscodeToHLS` starts ffmpeg in its own
  goroutine (`exec.CommandContext(ctx, …)`) and nothing joins it, so the disk guard, the
  `ctx.Done()` path and the ffmpeg-error path all return while ffmpeg is still running. Asynq
  cancels the task context right after the handler returns, which SIGKILLs the child — but the exit
  status is dropped (nobody reads `cmd.Wait()`) and the deferred `os.RemoveAll` of
  `/tmp/<uuid>` races with the dying process. Leftovers live until the next attempt or until the
  pod is recycled (the `/tmp` mount is an `emptyDir`).
- **Hardcoded metadata output.** `UpdateStreamMetadata` always reports `Format: "hls"` and
  `Resolution: "1280x720"`, so the resolution shown in the UI does not reflect the real encode
  (the VAAPI/libx264 bitrate and size limits make 1280x720 wrong for most sources).
- **`UploadDir` swallows walk errors.** `internal/storage/minio_storage.go` returns `nil` when the
  `filepath.WalkDir` callback fails, so a read error on the local HLS directory is silently
  treated as success and the stream is marked `ready` with an incomplete upload.
- **`make logs` selects the wrong label.** The target uses `-l app=transcoder`, but the
  Deployment/pod label is `app=transcoder-service`, so the command returns nothing.
- **`make all` never runs the tests.** It is `build → push → deploy`, so a broken build is shipped
  to the cluster before `make test` is ever run. `make deploy` also ends at `kubectl set image`
  without waiting for the rollout, and it re-uses the `git describe` tag, so rebuilding an
  unchanged tree makes `set image` a silent no-op (see Commands for the `rollout restart`
  workaround).
- **Makefile `.PHONY` is out of sync.** It lists a non-existent `clean` target and omits the real
  `test`, `logs`, and `keda-deploy` targets.
- **Dockerfile `EXPOSE 8080` is stale.** The worker serves no application port; metrics are on
  9090 in Kubernetes. `EXPOSE` is documentation-only and has no effect.
- **`scripts/test-local-cover.sh` hardcodes a WSL path** (`/mnt/c/Users/XOMRKOB/Desktop/...`) for the
  HTML report, so it fails anywhere else.
- **Encoder misconfiguration is not fatal.** An unknown `TRANSCODER_ENCODER` value, or `vaapi`
  without a render node, logs a warning and silently encodes with `libx264`. Check the
  `transcode encoder selected` startup log to confirm which encoder is actually in use.
- **Shared `default` queue.** KEDA cannot distinguish transcode tasks from any other producer on
  the asynq `default` queue, so an unrelated backlog can scale this worker up.
- **Task-type/type name typos.** The exported task constant is correctly spelled
  `TaskVideoTranscoding` and the method is `HandleVideoTranscoderTask`, but the receiver *type* is
  `HandleVideoTrancoder` ("Trancoder") while its constructor is
  `NewHandleVideoTranscoder` (correctly spelled). `NewFFmpegProcessor` fails hard if `ffmpeg` is not
  on `PATH`; only the *encoder* degrades gracefully.
