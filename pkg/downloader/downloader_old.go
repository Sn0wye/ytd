package downloader

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/kkdai/youtube/v2"
	"github.com/vbauerster/mpb/v5"
	"github.com/vbauerster/mpb/v5/decor"
)

type Downloader struct {
	youtube.Client
	OutputDir string
}

// func DownloadVideo(video *youtube.Video, format *youtube.Format, output string) error {
// 	// downloader := ytdl.Downloader{}

// 	err := DownloadComposite(context.Background(), output, video, format.Quality, format.MimeType, format.LanguageDisplayName())
// 	if err != nil {
// 		return err
// 	}

// 	// progress := mpb.New(mpb.WithWidth(60))

// 	// stream, size, err := downloader.GetStream(video, format)
// 	// if err != nil {
// 	// 	return err
// 	// }
// 	// defer stream.Close()

// 	// // Create a new progress bar
// 	// bar := progress.AddBar(size,
// 	// 	mpb.PrependDecorators(
// 	// 		decor.Name("Downloading: ", decor.WC{W: len("Downloading: "), C: decor.DidentRight}),
// 	// 		decor.CountersKibiByte("%.2f / %.2f"),
// 	// 	),
// 	// 	mpb.AppendDecorators(
// 	// 		decor.EwmaETA(decor.ET_STYLE_GO, 90),
// 	// 		decor.Name(" | "),
// 	// 		decor.EwmaSpeed(decor.UnitKiB, "%.2f", 60),
// 	// 	),
// 	// )

// 	// // Create the output file
// 	// out, err := os.Create(output)
// 	// if err != nil {
// 	// 	return err
// 	// }
// 	// defer out.Close()

// 	// // Create a reader that updates the progress bar
// 	// reader := bar.ProxyReader(stream)

// 	// // Start copying the stream to the file
// 	// _, err = io.Copy(out, reader)
// 	// if err != nil {
// 	// 	return err
// 	// }

// 	// // Wait for the progress bar to finish
// 	// progress.Wait()
// 	return nil
// }

func (dl *Downloader) DownloadComposite(ctx context.Context, outputFile string, v *youtube.Video, quality string, mimetype, language string) error {
	videoFormat, audioFormat, err1 := dl.getVideoAudioFormats(v, quality, mimetype, language)
	if err1 != nil {
		return err1
	}

	log := youtube.Logger.With("id", v.ID)

	log.Info(
		"Downloading composite video",
		"videoQuality", videoFormat.QualityLabel,
		"videoMimeType", videoFormat.MimeType,
		"audioMimeType", audioFormat.MimeType,
	)

	destFile, err := dl.getOutputFile(v, videoFormat, outputFile)
	if err != nil {
		return err
	}
	outputDir := filepath.Dir(destFile)

	// Create temporary video file
	videoFile, err := os.CreateTemp(outputDir, "youtube_*.m4v")
	if err != nil {
		return err
	}
	defer os.Remove(videoFile.Name())

	// Create temporary audio file
	audioFile, err := os.CreateTemp(outputDir, "youtube_*.m4a")
	if err != nil {
		return err
	}
	defer os.Remove(audioFile.Name())

	log.Debug("Downloading video file...")
	err = dl.videoDLWorker(ctx, videoFile, v, videoFormat)
	if err != nil {
		return err
	}

	log.Debug("Downloading audio file...")
	err = dl.videoDLWorker(ctx, audioFile, v, audioFormat)
	if err != nil {
		return err
	}

	//nolint:gosec
	ffmpegVersionCmd := exec.Command("ffmpeg", "-y",
		"-i", videoFile.Name(),
		"-i", audioFile.Name(),
		"-c", "copy", // Just copy without re-encoding
		"-shortest", // Finish encoding when the shortest input stream ends
		destFile,
		"-loglevel", "warning",
	)
	ffmpegVersionCmd.Stderr = os.Stderr
	ffmpegVersionCmd.Stdout = os.Stdout
	log.Info("merging video and audio", "output", destFile)

	return ffmpegVersionCmd.Run()
}

func (dl *Downloader) getVideoAudioFormats(v *youtube.Video, quality string, mimetype, language string) (*youtube.Format, *youtube.Format, error) {
	var videoFormats, audioFormats youtube.FormatList

	formats := v.Formats
	if mimetype != "" {
		formats = formats.Type(mimetype)
	}

	videoFormats = formats.Type("video").AudioChannels(0)
	audioFormats = formats.Type("audio")

	if quality != "" {
		videoFormats = videoFormats.Quality(quality)
	}

	if language != "" {
		audioFormats = audioFormats.Language(language)
	}

	if len(videoFormats) == 0 {
		return nil, nil, errors.New("no video format found after filtering")
	}

	if len(audioFormats) == 0 {
		return nil, nil, errors.New("no audio format found after filtering")
	}

	videoFormats.Sort()
	audioFormats.Sort()

	return &videoFormats[0], &audioFormats[0], nil
}

func (dl *Downloader) videoDLWorker(ctx context.Context, out *os.File, video *youtube.Video, format *youtube.Format) error {
	stream, size, err := dl.GetStreamContext(ctx, video, format)
	if err != nil {
		return err
	}

	prog := &progress{
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

func (dl *Downloader) getOutputFile(v *youtube.Video, format *youtube.Format, outputFile string) (string, error) {
	if outputFile == "" {
		outputFile = SanitizeFilename(v.Title)
		outputFile += pickIdealFileExtension(format.MimeType)
	}

	if dl.OutputDir != "" {
		if err := os.MkdirAll(dl.OutputDir, 0o755); err != nil {
			return "", err
		}
		outputFile = filepath.Join(dl.OutputDir, outputFile)
	}

	return outputFile, nil
}
