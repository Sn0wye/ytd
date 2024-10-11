package downloader

import (
	"context"
	"fmt"
	"os"

	"github.com/kkdai/youtube/v2"
)

func (dl *Downloader) DownloadVideo(ctx context.Context, outputVideoFile string, video *youtube.Video, itag int) error {
	fmt.Println("Starting video download...")
	videoFormat := dl.getFormatByItag(video, itag)

	if videoFormat == nil {
		return fmt.Errorf("video format with itag %d not found", itag)
	}

	// Create output video file
	outFile, err := os.Create(outputVideoFile)
	if err != nil {
		return err
	}
	defer outFile.Close()

	// Download the video using the format and store in outFile
	err = dl.videoDLWorker(ctx, outFile, video, videoFormat)
	if err != nil {
		return fmt.Errorf("error downloading video: %w", err)
	}

	fmt.Println("Video download completed.")
	return nil
}

func (dl *Downloader) DownloadAudio(ctx context.Context, outputAudioFile string, video *youtube.Video, itag int) error {
	fmt.Println("Starting audio download...")
	audioFormat := dl.getFormatByItag(video, itag)

	if audioFormat == nil {
		return fmt.Errorf("audio format with itag %d not found", itag)
	}

	// Create output audio file
	outFile, err := os.Create(outputAudioFile)
	if err != nil {
		return err
	}
	defer outFile.Close()

	// Download the audio using the format and store in outFile
	err = dl.videoDLWorker(ctx, outFile, video, audioFormat)
	if err != nil {
		return fmt.Errorf("error downloading audio: %w", err)
	}

	fmt.Println("Audio download completed.")
	return nil
}

// getFormatByItag finds the format by itag in the video's available formats
func (dl *Downloader) getFormatByItag(video *youtube.Video, itag int) *youtube.Format {
	for _, format := range video.Formats {
		if format.ItagNo == itag {
			return &format
		}
	}
	return nil
}
