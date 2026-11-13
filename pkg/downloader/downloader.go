// Package downloader turns one YouTube video into one file: it picks the
// streams that fit the output format, downloads them and muxes them (or
// re-encodes with ffmpeg for mp3 and --h264). The terminal command and the
// GUI share it and only differ in how they show progress (Reporter) and how
// the streams are chosen (prompts, --itag or best).
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

// IsKnown reports whether c is a supported output format.
func IsKnown(c string) bool {
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
		if ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(fileName), ".")); IsKnown(ext) {
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

// Fits reports whether f can go into container without re-encoding.
func Fits(f youtube.Format, container string) bool {
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
		if Fits(f, container) {
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

// Selection asks for specific streams; 0 means the best usable one.
type Selection struct {
	VideoItag, AudioItag int
}

// Select picks streams without prompting: the requested itags, or the best
// usable streams. vf is nil for audio-only output.
func Select(v *youtube.Video, o Options, s Selection) (vf *youtube.Format, af youtube.Format, err error) {
	if !o.AudioOnly() {
		all, usable := VideoChoices(v, o)
		var f youtube.Format
		switch {
		case s.VideoItag != 0:
			f, err = PickItag(all, usable, s.VideoItag, o.Container)
		case len(usable) == 0:
			err = fmt.Errorf("no video streams can be saved as %s; try --format mkv", o.Container)
		default:
			f = usable[0]
		}
		if err != nil {
			return nil, af, err
		}
		vf = &f
	}
	all, usable := AudioChoices(v, o)
	switch {
	case s.AudioItag != 0:
		af, err = PickItag(all, usable, s.AudioItag, o.Container)
	case len(usable) == 0:
		err = fmt.Errorf("no audio streams can be saved as %s", o.Container)
	default:
		af = usable[0]
	}
	return vf, af, err
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
	if vf != nil && o.ForceH264 && vf.Codec == "h264" && Fits(af, o.Container) {
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
	if IsKnown(ext) {
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	return name
}

// OutputPath returns a path in dir that does not exist yet; existing files
// are never overwritten.
func OutputPath(dir, fileName, title, id, container string) string {
	name := Name(fileName, title, id)
	path := filepath.Join(dir, name+"."+container)
	for i := 1; ; i++ {
		if _, err := os.Stat(path); err != nil {
			// Let the writer report inaccessible or invalid paths instead of looping.
			return path
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.%s", name, i, container))
	}
}

// Exists reports whether a file for title was already saved in dir as
// container, so a playlist can be resumed.
func Exists(dir, title, id, container string) bool {
	_, err := os.Stat(filepath.Join(dir, Name("", title, id)+"."+container))
	return err == nil
}

// Fetch gets the video and downloads it with the streams chosen by sel. A
// 403 on the stream URLs is retried once with freshly fetched URLs. onVideo,
// if set, is called after each successful fetch.
func Fetch(ctx context.Context, c *youtube.Client, id string, o Options, sel Selection, r Reporter, onVideo func(*youtube.Video)) (string, *youtube.Video, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		v, err := c.GetVideo(ctx, id)
		if err != nil {
			return "", nil, err
		}
		if onVideo != nil {
			onVideo(v)
		}
		vf, af, err := Select(v, o, sel)
		if err != nil {
			return "", v, err
		}
		path, err := Run(ctx, c, v, vf, af, o, r)
		if err == nil {
			return path, v, nil
		}
		if !errors.Is(err, youtube.ErrForbidden) {
			return "", v, err
		}
		lastErr = err
	}
	return "", nil, lastErr
}
