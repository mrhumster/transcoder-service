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
	// TranscodeChunk encodes [startSec, startSec+durSec) of the source into
	// outputDir. Unlike TranscodeToHLS the reported Percent is mapped onto the
	// whole video, so a caller looping over chunks sees one continuous 0..100
	// progress bar instead of each chunk restarting from zero.
	TranscodeChunk(ctx context.Context, inputPath, outputDir string, startSec, durSec float64) (<-chan Progress, <-chan error)
	GetDuration(ctx context.Context, inputPath string) (float64, error)
	ProbeMetadata(ctx context.Context, inputPath string) (VideoMetadata, error)
}
