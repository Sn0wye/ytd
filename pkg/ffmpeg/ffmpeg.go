package ffmpeg

import (
	"os/exec"
)

// MergeVideoWithAudio merges a video file with an audio file into a single output file.
func MergeVideoWithAudio(videoFile, audioFile, outputFile string) error {
	cmd := exec.Command("ffmpeg", "-i", videoFile, "-i", audioFile, "-c:v", "copy", "-c:a", "aac", outputFile)
	return cmd.Run()
}
