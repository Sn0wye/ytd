// Package downloader turns one YouTube video into one file: it picks the
// streams that fit the output format, downloads them and muxes them (or
// re-encodes with ffmpeg for mp3 and --h264). Single videos and playlist
// entries share it and only differ in how they show progress (Reporter) and
// how the streams are chosen (prompts and --itag, or the best ones).
package downloader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Sn0wye/ytd/pkg/ffmpeg"
	"github.com/Sn0wye/ytd/pkg/media"
	"github.com/Sn0wye/ytd/pkg/utils"
	"github.com/Sn0wye/ytd/pkg/youtube"
)

var (
	VideoContainers = []string{"mp4", "mkv", "webm"}
	AudioContainers = []string{"m4a", "opus", "mp3"}
)

// isKnown reports whether c is a supported output format.
func isKnown(c string) bool {
	return slices.Contains(VideoContainers, c) || slices.Contains(AudioContainers, c)
}

// Options says what file to produce.
type Options struct {
	Container string // a resolved format, see ResolveContainer
	ForceH264 bool
	OutputDir string
	TmpDir    string
	FileName  string // optional; default is the slugified title
}

// AudioOnly reports whether the output has no video.
func (o Options) AudioOnly() bool { return slices.Contains(AudioContainers, o.Container) }

// transcodes reports whether ffmpeg may be needed: always for mp3, and for
// --h264 unless the chosen video already is H.264 (see Run).
func (o Options) transcodes() bool { return o.Container == "mp3" || o.ForceH264 }

// ResolveContainer picks the output format from format, the fileName
// extension, or the default, and reconciles it with audioOnly and h264.
func ResolveContainer(format, fileName string, audioOnly, h264 bool) (string, error) {
	c := strings.ToLower(strings.TrimPrefix(format, "."))
	if c == "" {
		if ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(fileName), ".")); isKnown(ext) {
			c = ext
		}
	}
	if c == "" {
		if audioOnly {
			return "m4a", nil
		}
		return "mp4", nil
	}

	switch {
	case slices.Contains(AudioContainers, c):
		audioOnly = true // e.g. --format mp3 implies audio only
	case slices.Contains(VideoContainers, c):
		if audioOnly {
			return "", fmt.Errorf("--audio-only needs an audio format (%s), got %q", strings.Join(AudioContainers, ", "), c)
		}
	default:
		return "", fmt.Errorf("unsupported format %q (use one of: %s, %s)", c,
			strings.Join(VideoContainers, ", "), strings.Join(AudioContainers, ", "))
	}
	if h264 && audioOnly {
		return "", errors.New("--h264 has no effect on audio-only downloads")
	}
	if h264 && c == "webm" {
		return "", errors.New("--h264 cannot be used with webm (webm only holds VP9/AV1); use mp4 or mkv")
	}
	return c, nil
}

// VideoChoices returns all video streams and those usable for o, best first.
func VideoChoices(v *youtube.Video, o Options) (all, usable []youtube.Format) {
	all = v.VideoFormats()
	return all, compatible(all, o.Container, o.transcodes())
}

// AudioChoices returns all audio streams and those usable for o, best first.
// With --h264 the audio is still copied, so it must fit the container.
func AudioChoices(v *youtube.Video, o Options) (all, usable []youtube.Format) {
	all = v.AudioFormats()
	return all, compatible(all, o.Container, o.transcodes() && !o.ForceH264)
}

// fits reports whether f can go into container without re-encoding.
func fits(f youtube.Format, container string) bool {
	return media.Supports(container, f.Codec, f.Ext)
}

// compatible keeps the formats that can go into container. When ffmpeg
// re-encodes, every stream is usable.
func compatible(list []youtube.Format, container string, transcode bool) []youtube.Format {
	if transcode {
		return list
	}
	var out []youtube.Format
	for _, f := range list {
		if fits(f, container) {
			out = append(out, f)
		}
	}
	return out
}

// Hidden describes the codecs that were left out of a selector.
func Hidden(all, shown []youtube.Format) string {
	var labels []string
	for _, f := range all {
		if !slices.ContainsFunc(shown, func(s youtube.Format) bool { return s.Itag == f.Itag }) && !slices.Contains(labels, f.CodecLabel()) {
			labels = append(labels, f.CodecLabel())
		}
	}
	return strings.Join(labels, "/")
}

// PickItag returns the format with the given itag from the usable list.
func PickItag(all, usable []youtube.Format, itag int, container string) (youtube.Format, error) {
	for _, f := range usable {
		if f.Itag == itag {
			return f, nil
		}
	}
	for _, f := range all {
		if f.Itag == itag {
			return youtube.Format{}, fmt.Errorf("itag %d (%s) cannot be saved as %s without re-encoding", itag, f.CodecLabel(), container)
		}
	}
	return youtube.Format{}, fmt.Errorf("itag %d is not available for this video", itag)
}

// Best picks the best usable streams without prompting. vf is nil for
// audio-only output.
func Best(v *youtube.Video, o Options) (vf *youtube.Format, af youtube.Format, err error) {
	if !o.AudioOnly() {
		_, usable := VideoChoices(v, o)
		if len(usable) == 0 {
			return nil, af, fmt.Errorf("no video streams can be saved as %s; try --format mkv", o.Container)
		}
		vf = &usable[0]
	}
	_, usable := AudioChoices(v, o)
	if len(usable) == 0 {
		return nil, af, fmt.Errorf("no audio streams can be saved as %s", o.Container)
	}
	return vf, usable[0], nil
}

// Stage is a step of Run.
type Stage int

const (
	StageDownload  Stage = iota // Bytes reports progress
	StageTranscode              // ffmpeg; Media reports progress
	StageMux                    // pure Go muxing, no progress
)

// Reporter shows the progress of Run. Bytes and Media may be called from
// another goroutine than the one that called Run.
type Reporter interface {
	Begin(s Stage)
	Bytes(done, total int64)
	Media(done, total time.Duration)
	// End closes the stage begun last; err is nil on success.
	End(s Stage, err error)
}

// Run downloads vf (nil for audio only) and af and writes the output file,
// returning its path.
func Run(ctx context.Context, c *youtube.Client, v *youtube.Video, vf *youtube.Format, af youtube.Format, o Options, r Reporter) (string, error) {
	transcode := o.transcodes()
	// --h264 with a stream that already is H.264 needs no re-encode.
	if vf != nil && o.ForceH264 && vf.Codec == "h264" && fits(af, o.Container) {
		transcode = false
	}
	if transcode {
		if err := ffmpeg.CheckInstalled(); err != nil {
			return "", fmt.Errorf("%s output needs ffmpeg to re-encode: %w", o.Container, err)
		}
	}

	tmp := o.TmpDir
	if tmp == "" {
		tmp = os.TempDir()
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	workDir, err := os.MkdirTemp(tmp, "ytd-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workDir)

	var jobs []youtube.Job
	var sources []media.Source
	if vf != nil {
		p := filepath.Join(workDir, "video."+vf.Ext)
		jobs = append(jobs, youtube.Job{Format: *vf, Path: p})
		sources = append(sources, media.Source{Path: p, Ext: vf.Ext})
	}
	audioPath := filepath.Join(workDir, "audio."+af.Ext)
	jobs = append(jobs, youtube.Job{Format: af, Path: audioPath})
	sources = append(sources, media.Source{Path: audioPath, Ext: af.Ext})

	r.Begin(StageDownload)
	err = c.Download(ctx, jobs, r.Bytes)
	r.End(StageDownload, err)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(o.OutputDir, 0o755); err != nil {
		return "", err
	}
	output := OutputPath(o.OutputDir, o.FileName, v.Title, v.ID, o.Container)
	meta := media.Meta{Title: v.Title, Artist: v.Author}

	if transcode {
		job := ffmpeg.Job{
			AudioPath:  audioPath,
			AudioCodec: af.Codec,
			Container:  o.Container,
			ForceH264:  o.ForceH264,
			Title:      meta.Title,
			Artist:     meta.Artist,
			Output:     output,
			Progress:   func(done time.Duration) { r.Media(done, v.Duration) },
		}
		if vf != nil {
			job.VideoPath = sources[0].Path
			job.VideoCodec = vf.Codec
			job.VideoBitrate = float64(vf.Bitrate) / 1000
		}
		r.Begin(StageTranscode)
		err := ffmpeg.Run(ctx, job)
		r.End(StageTranscode, err)
		if err != nil {
			os.Remove(output)
			return "", err
		}
		return output, nil
	}

	r.Begin(StageMux)
	err = media.Mux(output, o.Container, sources, meta)
	r.End(StageMux, err)
	if err != nil {
		return "", fmt.Errorf("writing %s: %w", o.Container, err)
	}
	return output, nil
}

// Name is the file name (without extension) used for a video: fileName if
// given, else the slugified title, else the video ID.
func Name(fileName, title, id string) string {
	name := fileName
	if name == "" {
		name = utils.SlugifyFilename(title)
	}
	if name == "" {
		name = id
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if isKnown(ext) {
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	return name
}

// numbered is dir/name.container for n == 0 and dir/name-n.container after,
// the names OutputPath tries in order.
func numbered(dir, name, container string, n int) string {
	if n == 0 {
		return filepath.Join(dir, name+"."+container)
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d.%s", name, n, container))
}

// OutputPath returns a path in dir that does not exist yet; existing files
// are never overwritten.
func OutputPath(dir, fileName, title, id, container string) string {
	name := Name(fileName, title, id)
	for n := 0; ; n++ {
		path := numbered(dir, name, container, n)
		if _, err := os.Stat(path); err != nil {
			// Let the writer report inaccessible or invalid paths instead of looping.
			return path
		}
	}
}

// Exists reports whether a video named name (see Name) was already saved in
// dir as container, so a playlist can be resumed. dup counts the earlier
// videos of the playlist with the same name: they were saved first and took
// name.container, name-1.container and so on.
func Exists(dir, name, container string, dup int) bool {
	_, err := os.Stat(numbered(dir, name, container, dup))
	return err == nil
}

// FetchBest gets the video and downloads its best streams for o. A 403 on
// the stream URLs is retried once with freshly fetched URLs.
func FetchBest(ctx context.Context, c *youtube.Client, id string, o Options, r Reporter) (string, error) {
	for attempt := 1; ; attempt++ {
		v, err := c.GetVideo(ctx, id)
		if err != nil {
			return "", err
		}
		vf, af, err := Best(v, o)
		if err != nil {
			return "", err
		}
		path, err := Run(ctx, c, v, vf, af, o, r)
		if attempt == 2 || !errors.Is(err, youtube.ErrForbidden) {
			return path, err
		}
	}
}
