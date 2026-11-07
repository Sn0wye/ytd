// Package ffmpeg builds and runs the ffmpeg command that turns the
// downloaded streams into the final file, copying streams whenever the
// target container supports them and re-encoding only when it must.
package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Binary is the ffmpeg executable name or path.
var Binary = "ffmpeg"

// VideoContainers are the output formats that keep video and audio.
var VideoContainers = []string{"mp4", "mkv", "webm"}

// AudioContainers are the output formats for audio-only downloads.
var AudioContainers = []string{"m4a", "mp3", "opus"}

// IsVideoContainer reports whether c is a supported video output format.
func IsVideoContainer(c string) bool { return slices.Contains(VideoContainers, c) }

// IsAudioContainer reports whether c is a supported audio-only output format.
func IsAudioContainer(c string) bool { return slices.Contains(AudioContainers, c) }

// Job describes one conversion. Codecs use the normalized names from
// ytdlp.NormalizeCodec (h264, av1, vp9, hevc, aac, opus, ...).
type Job struct {
	VideoPath    string // empty for audio-only jobs
	VideoCodec   string
	VideoBitrate float64 // kbit/s of the source, used when re-encoding to H.264
	AudioPath    string
	AudioCodec   string
	Container    string
	ForceH264    bool // re-encode non-H.264 video for maximum player compatibility
	Title        string
	Artist       string
	Output       string
}

// H264Encoder is the encoder used when video must be re-encoded.
// macOS gets the hardware encoder, which is many times faster than libx264.
var H264Encoder = func() string {
	if runtime.GOOS == "darwin" {
		return "h264_videotoolbox"
	}
	return "libx264"
}()

// Args returns the ffmpeg arguments for the job.
func Args(j Job) ([]string, error) {
	if j.AudioPath == "" {
		return nil, errors.New("an audio stream is required")
	}
	audioOnly := j.VideoPath == ""
	if audioOnly && !IsAudioContainer(j.Container) {
		return nil, fmt.Errorf("unsupported audio format %q (use one of: %s)", j.Container, strings.Join(AudioContainers, ", "))
	}
	if !audioOnly && !IsVideoContainer(j.Container) {
		return nil, fmt.Errorf("unsupported video format %q (use one of: %s)", j.Container, strings.Join(VideoContainers, ", "))
	}

	args := []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error", "-stats"}
	if audioOnly {
		args = append(args, "-i", j.AudioPath, "-map", "0:a:0", "-vn")
	} else {
		args = append(args, "-i", j.VideoPath, "-i", j.AudioPath, "-map", "0:v:0", "-map", "1:a:0")
		v, err := videoArgs(j)
		if err != nil {
			return nil, err
		}
		args = append(args, v...)
	}
	args = append(args, audioArgs(j)...)

	// Drop source metadata (e.g. "ISO Media file produced by Google") and set our own.
	args = append(args, "-map_metadata", "-1")
	if j.Title != "" {
		args = append(args, "-metadata", "title="+j.Title)
	}
	if j.Artist != "" {
		args = append(args, "-metadata", "artist="+j.Artist)
	}

	switch j.Container {
	case "mp4", "m4a":
		args = append(args, "-movflags", "+faststart", "-f", "mp4")
	case "mkv":
		args = append(args, "-f", "matroska")
	case "webm":
		args = append(args, "-f", "webm")
	case "mp3":
		args = append(args, "-f", "mp3")
	case "opus":
		args = append(args, "-f", "opus")
	}
	return append(args, j.Output), nil
}

func videoArgs(j Job) ([]string, error) {
	switch j.Container {
	case "webm":
		if j.ForceH264 {
			return nil, errors.New("--h264 cannot be used with webm (webm only holds VP9/AV1); use mp4 or mkv")
		}
		if j.VideoCodec != "vp9" && j.VideoCodec != "av1" {
			return nil, fmt.Errorf("webm needs VP9 or AV1 video, but the selected stream is %s; pick another video format or use mp4/mkv", j.VideoCodec)
		}
		return []string{"-c:v", "copy"}, nil
	}

	if j.ForceH264 && j.VideoCodec != "h264" {
		return h264Args(j.VideoBitrate), nil
	}
	args := []string{"-c:v", "copy"}
	if j.VideoCodec == "hevc" && j.Container == "mp4" {
		args = append(args, "-tag:v", "hvc1") // needed for QuickTime/Apple players
	}
	return args, nil
}

func h264Args(srcKbps float64) []string {
	args := []string{"-c:v", H264Encoder, "-pix_fmt", "yuv420p"}
	if H264Encoder == "libx264" {
		return append(args, "-preset", "fast", "-crf", "18")
	}
	// Hardware encoders are bitrate driven. H.264 needs roughly 1.5x the
	// bitrate of VP9/AV1 for similar quality.
	kbps := int(srcKbps * 1.5)
	if kbps < 1500 {
		kbps = 1500
	}
	return append(args, "-b:v", strconv.Itoa(kbps)+"k")
}

// audioArgs copies the audio when the container accepts it and transcodes otherwise.
func audioArgs(j Job) []string {
	copyable := map[string][]string{
		"mp4":  {"aac", "ac3", "eac3", "mp3"},
		"m4a":  {"aac"},
		"mkv":  {"aac", "opus", "ac3", "eac3", "mp3", "vorbis"},
		"webm": {"opus", "vorbis"},
		"mp3":  {"mp3"},
		"opus": {"opus"},
	}
	if slices.Contains(copyable[j.Container], j.AudioCodec) {
		return []string{"-c:a", "copy"}
	}
	switch j.Container {
	case "webm", "opus":
		return []string{"-c:a", "libopus", "-b:a", "160k"}
	case "mp3":
		return []string{"-c:a", "libmp3lame", "-q:a", "0"}
	default:
		return []string{"-c:a", "aac", "-b:a", "192k"}
	}
}

// CheckInstalled returns a helpful error when ffmpeg is missing.
func CheckInstalled() error {
	if _, err := exec.LookPath(Binary); err != nil {
		return errors.New("ffmpeg not found in PATH (install it with `brew install ffmpeg`)")
	}
	return nil
}

// Run executes the job. ffmpeg's progress line and errors go to stderr.
func Run(ctx context.Context, j Job) error {
	args, err := Args(j)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, Binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	if err := cmd.Run(); err != nil {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		return fmt.Errorf("ffmpeg failed: %s", strings.TrimSpace(lines[len(lines)-1]))
	}
	return nil
}
