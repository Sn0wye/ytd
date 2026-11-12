// Package ffmpeg builds and runs the ffmpeg command that turns the
// downloaded streams into the final file, copying streams whenever the
// target container supports them and re-encoding only when it must.
package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Binary is the ffmpeg executable name or path.
var Binary = "ffmpeg"

var binaryMu sync.Mutex

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

	// Progress, if set, is called with how much of the media ffmpeg has
	// written so far, a few times per second.
	Progress func(done time.Duration)
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

	// Progress goes to stdout as key=value lines; see Run.
	args := []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error", "-nostats", "-progress", "pipe:1"}
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

// CheckInstalled returns a helpful error when ffmpeg is missing. On Windows
// it also looks where winget installs ffmpeg, because `winget install` only
// updates PATH for programs started afterwards (and never for Explorer
// windows that were already open).
func CheckInstalled() error {
	binaryMu.Lock()
	defer binaryMu.Unlock()
	if _, err := exec.LookPath(Binary); err == nil {
		return nil
	}
	if runtime.GOOS == "windows" && Binary == "ffmpeg" {
		if p := wingetFFmpeg(os.Getenv("LOCALAPPDATA")); p != "" {
			Binary = p
			return nil
		}
	}
	return errors.New("ffmpeg not found in PATH (install it with `" + InstallHint(runtime.GOOS) + "`)")
}

// wingetFFmpeg returns ffmpeg.exe from winget's links folder or package
// folders under localAppData, or "" if there is none.
func wingetFFmpeg(localAppData string) string {
	if localAppData == "" {
		return ""
	}
	winget := filepath.Join(localAppData, "Microsoft", "WinGet")
	candidates := []string{filepath.Join(winget, "Links", "ffmpeg.exe")}
	for _, pattern := range []string{
		filepath.Join(winget, "Packages", "*FFmpeg*", "*", "bin", "ffmpeg.exe"),
		filepath.Join(winget, "Packages", "*FFmpeg*", "bin", "ffmpeg.exe"),
	} {
		matches, _ := filepath.Glob(pattern)
		slices.Sort(matches)
		slices.Reverse(matches) // newest version first
		candidates = append(candidates, matches...)
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// InstallHint is the usual command to install ffmpeg on goos.
func InstallHint(goos string) string {
	switch goos {
	case "windows":
		return "winget install Gyan.FFmpeg"
	case "darwin":
		return "brew install ffmpeg"
	}
	return "sudo apt install ffmpeg"
}

// Run executes the job, reporting progress to j.Progress. ffmpeg's errors
// are returned, not printed.
func Run(ctx context.Context, j Job) error {
	args, err := Args(j)
	if err != nil {
		return err
	}
	binaryMu.Lock()
	binary := Binary
	binaryMu.Unlock()
	cmd := exec.CommandContext(ctx, binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if d, ok := progressTime(sc.Text()); ok && j.Progress != nil {
			j.Progress(d)
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		return fmt.Errorf("ffmpeg failed: %s", strings.TrimSpace(lines[len(lines)-1]))
	}
	return nil
}

// progressTime parses the out_time_us line of ffmpeg's -progress output.
// (out_time_ms holds microseconds too, a long-standing ffmpeg quirk.)
func progressTime(line string) (time.Duration, bool) {
	v, ok := strings.CutPrefix(line, "out_time_us=")
	if !ok {
		return 0, false
	}
	us, err := strconv.ParseInt(v, 10, 64)
	if err != nil || us < 0 {
		return 0, false // "N/A" before the first frame
	}
	return time.Duration(us) * time.Microsecond, true
}
