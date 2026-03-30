//go:generate mockgen -source=processor.go -destination=mock/processor_mock.go -package=mock
package processor

import "context"

type VideoProcessor interface {
	TranscodeToHLS(ctx context.Context, inputPath, outputDir string) error
	GetDuration(ctx context.Context, inputPath string) (float64, error)
}
