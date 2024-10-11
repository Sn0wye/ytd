package formatter

import (
	"strings"

	"github.com/kkdai/youtube/v2"
)

type VideoFormat struct {
	Itag          int
	FPS           int
	VideoQuality  string
	AudioQuality  string
	AudioChannels int
	Language      string
	Size          int64
	Bitrate       int
	MimeType      string
	Format        *youtube.Format
}

type VideoInfo struct {
	ID       string
	Title    string
	Author   string
	Duration string
	Formats  []VideoFormat
}

func FormatVideo(video *youtube.Video) VideoInfo {
	videoInfo := VideoInfo{
		Title:    video.Title,
		Author:   video.Author,
		Duration: video.Duration.String(),
	}

	for _, format := range video.Formats {
		bitrate := format.AverageBitrate
		if bitrate == 0 {
			// Some formats don't have the average bitrate
			bitrate = format.Bitrate
		}

		size := format.ContentLength
		if size == 0 {
			// Some formats don't have this information
			size = int64(float64(bitrate) * video.Duration.Seconds() / 8)
		}

		videoInfo.Formats = append(videoInfo.Formats, VideoFormat{
			Itag:          format.ItagNo,
			FPS:           format.FPS,
			VideoQuality:  format.QualityLabel,
			AudioQuality:  strings.ToLower(strings.TrimPrefix(format.AudioQuality, "AUDIO_QUALITY_")),
			AudioChannels: format.AudioChannels,
			Size:          size,
			Bitrate:       bitrate,
			MimeType:      format.MimeType,
			Language:      format.LanguageDisplayName(),
			Format:        &format,
		})
	}

	return videoInfo
}
