//go:generate mockgen -source=processor.go -destination=mock/processor_mock.go -package=mock
package processor

import "context"

type Progress struct {
	Percent  float64
	Current  string
	Frames   int64
	Finished bool
}

type VideoProcessor interface {
	TranscodeToHLS(ctx context.Context, inputPath, outputDir string) (<-chan Progress, <-chan error)
	GetDuration(ctx context.Context, inputPath string) (float64, error)
}
