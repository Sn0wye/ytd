package downloader

import (
	"context"
	"fmt"
	"os"

	"io"

	"github.com/vbauerster/mpb/v5"
	"github.com/vbauerster/mpb/v5/decor"

	"github.com/kkdai/youtube/v2"
)

type Downloader struct {
	youtube.Client
	OutputDir string
}

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

func (dl *Downloader) videoDLWorker(ctx context.Context, out *os.File, video *youtube.Video, format *youtube.Format) error {
	stream, size, err := dl.GetStreamContext(ctx, video, format)
	if err != nil {
		return err
	}

	prog := &Progress{
		contentLength: float64(size),
	}

	// create progress bar
	progress := mpb.New(mpb.WithWidth(64))
	bar := progress.AddBar(
		int64(prog.contentLength),

		mpb.PrependDecorators(
			decor.CountersKibiByte("% .2f / % .2f"),
			decor.Percentage(decor.WCSyncSpace),
		),
		mpb.AppendDecorators(
			decor.EwmaETA(decor.ET_STYLE_GO, 90),
			decor.Name(" ] "),
			decor.EwmaSpeed(decor.UnitKiB, "% .2f", 60),
		),
	)

	reader := bar.ProxyReader(stream)
	mw := io.MultiWriter(out, prog)
	_, err = io.Copy(mw, reader)
	if err != nil {
		return err
	}

	progress.Wait()
	return nil
}
