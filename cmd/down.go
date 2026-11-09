package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Sn0wye/ytd/pkg/ffmpeg"
	"github.com/Sn0wye/ytd/pkg/media"
	"github.com/Sn0wye/ytd/pkg/ui"
	"github.com/Sn0wye/ytd/pkg/utils"
	"github.com/Sn0wye/ytd/pkg/youtube"
	"github.com/spf13/cobra"
)

var (
	videoContainers = []string{"mp4", "mkv", "webm"}
	audioContainers = []string{"m4a", "opus", "mp3"}
)

var (
	outputDir   string
	tmpDir      string
	fileName    string
	format      string
	audioOnly   bool
	pickBest    bool
	itags       string
	forceH264   bool
	connections int
	out         = ui.New()
)

var downCmd = &cobra.Command{
	Use:   "down <url>",
	Short: "Download a YouTube video, choosing video and audio quality",
	Long: `Download a YouTube video. You pick the video quality and the audio quality,
ytd downloads both streams in parallel and merges them into a single file in
the chosen format (mp4 by default). Streams are copied as-is, never
re-encoded, and no external program is needed.

Only mp3 output and --h264 need ffmpeg, because they re-encode.

Examples:
  ytd down https://www.youtube.com/watch?v=aqz-KE-bpKQ
  ytd down https://youtu.be/aqz-KE-bpKQ --best
  ytd down <url> --format webm
  ytd down <url> --audio-only            # m4a
  ytd down <url> --format opus`,
	Args: cobra.ExactArgs(1),
	Run: func(_ *cobra.Command, args []string) {
		err := download(args[0])
		if errors.Is(err, ui.ErrAborted) || errors.Is(err, context.Canceled) {
			out.Fail(ui.ErrAborted)
			os.Exit(130)
		}
		if err != nil {
			out.Fail(err)
			os.Exit(1)
		}
	},
}

func init() {
	defaultOut := "."
	if home, err := os.UserHomeDir(); err == nil {
		defaultOut = filepath.Join(home, "Downloads")
	}

	f := downCmd.Flags()
	f.StringVarP(&outputDir, "output-dir", "o", defaultOut, "directory for the final file")
	f.StringVarP(&fileName, "filename", "f", "", "output file name (default: slugified video title)")
	f.StringVar(&format, "format", "", "output format: "+strings.Join(videoContainers, ", ")+
		" (video) or "+strings.Join(audioContainers, ", ")+" (audio only); default mp4, or m4a with --audio-only")
	f.BoolVarP(&audioOnly, "audio-only", "a", false, "download only the audio")
	f.BoolVarP(&pickBest, "best", "b", false, "skip the prompts and pick the best quality")
	f.StringVarP(&itags, "itag", "i", "", "skip the prompts and pick streams by itag: VIDEO+AUDIO, or AUDIO with --audio-only")
	f.BoolVar(&forceH264, "h264", false, "re-encode VP9/AV1 video to H.264 so it plays everywhere (needs ffmpeg, slower)")
	f.IntVarP(&connections, "connections", "c", 8, "parallel connections per download")
	f.StringVarP(&tmpDir, "tmp-dir", "t", os.TempDir(), "directory for temporary files")
	rootCmd.AddCommand(downCmd)
}

func download(url string) error {
	start := time.Now()
	id, err := youtube.ExtractVideoID(url)
	if err != nil {
		return err
	}
	container, err := resolveContainer()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := youtube.NewClient(connections)
	out.Intro(version)
	stopTask := out.Task("Fetching video info", "Video found")
	video, err := client.GetVideo(ctx, id)
	stopTask(err)
	if err != nil {
		return err
	}
	out.Info(video.Title, video.Author, video.Duration)

	transcode := container == "mp3" || forceH264
	wantVideo, wantAudio, err := parseItags(itags)
	if err != nil {
		return err
	}
	var vf *youtube.Format
	if !audioOnly {
		v, err := chooseVideo(video, container, transcode, wantVideo)
		if err != nil {
			return err
		}
		vf = &v
	}
	af, err := chooseAudio(video, container, transcode, wantAudio)
	if err != nil {
		return err
	}
	// --h264 with a stream that already is H.264 needs no re-encode.
	if vf != nil && forceH264 && vf.Codec == "h264" && media.Supports(container, af.Codec, af.Ext) {
		transcode = false
	}
	if transcode {
		if err := ffmpeg.CheckInstalled(); err != nil {
			return fmt.Errorf("%s output needs ffmpeg to re-encode: %w", container, err)
		}
	}

	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	workDir, err := os.MkdirTemp(tmpDir, "ytd-")
	if err != nil {
		return err
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

	var total int64
	for _, j := range jobs {
		total += j.Format.Size
	}
	bar := out.StartProgress("Downloading")
	if err := client.Download(ctx, jobs, bar.Bytes); err != nil {
		bar.Fail()
		return err
	}
	bar.Done("Downloaded", fmt.Sprintf("%s in %.1fs", utils.FormatSize(total), bar.Elapsed().Seconds()))

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	output := outputPath(video, container)
	meta := media.Meta{Title: video.Title, Artist: video.Author}

	if transcode {
		bar := out.StartProgress("Re-encoding with ffmpeg")
		job := ffmpeg.Job{
			AudioPath:  audioPath,
			AudioCodec: af.Codec,
			Container:  container,
			ForceH264:  forceH264,
			Title:      meta.Title,
			Artist:     meta.Artist,
			Output:     output,
			Progress:   func(done time.Duration) { bar.Media(done, video.Duration) },
		}
		if vf != nil {
			job.VideoPath = sources[0].Path
			job.VideoCodec = vf.Codec
			job.VideoBitrate = float64(vf.Bitrate) / 1000
		}
		if err := ffmpeg.Run(ctx, job); err != nil {
			bar.Fail()
			os.Remove(output)
			return err
		}
		bar.Done("Re-encoded", fmt.Sprintf("in %.1fs", bar.Elapsed().Seconds()))
	} else {
		stopTask := out.Task("Muxing into "+container, "Muxed into "+container)
		err := media.Mux(output, container, sources, meta)
		stopTask(err)
		if err != nil {
			return fmt.Errorf("writing %s: %w", container, err)
		}
	}

	var size int64
	if st, err := os.Stat(output); err == nil {
		size = st.Size()
	}
	out.Saved(output, size, time.Since(start))
	return nil
}

// resolveContainer picks the output format from --format, the --filename
// extension, or the default, and reconciles it with --audio-only.
func resolveContainer() (string, error) {
	isKnown := func(c string) bool {
		return slices.Contains(videoContainers, c) || slices.Contains(audioContainers, c)
	}
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
	case slices.Contains(audioContainers, c):
		audioOnly = true // e.g. --format mp3 implies audio only
	case slices.Contains(videoContainers, c):
		if audioOnly {
			return "", fmt.Errorf("--audio-only needs an audio format (%s), got %q", strings.Join(audioContainers, ", "), c)
		}
	default:
		return "", fmt.Errorf("unsupported format %q (use one of: %s, %s)", c,
			strings.Join(videoContainers, ", "), strings.Join(audioContainers, ", "))
	}
	if forceH264 && audioOnly {
		return "", errors.New("--h264 has no effect on audio-only downloads")
	}
	if forceH264 && c == "webm" {
		return "", errors.New("--h264 cannot be used with webm (webm only holds VP9/AV1); use mp4 or mkv")
	}
	return c, nil
}

func outputPath(v *youtube.Video, container string) string {
	name := fileName
	if name == "" {
		name = utils.SlugifyFilename(v.Title)
	}
	if name == "" {
		name = v.ID
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if slices.Contains(videoContainers, ext) || slices.Contains(audioContainers, ext) {
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}

	// Never overwrite an existing file.
	path := filepath.Join(outputDir, name+"."+container)
	for i := 1; ; i++ {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path
		}
		path = filepath.Join(outputDir, fmt.Sprintf("%s-%d.%s", name, i, container))
	}
}

// compatible keeps the formats that can go into container. When ffmpeg
// re-encodes, every stream is usable.
func compatible(list []youtube.Format, container string, transcode bool) []youtube.Format {
	if transcode {
		return list
	}
	var out []youtube.Format
	for _, f := range list {
		if media.Supports(container, f.Codec, f.Ext) {
			out = append(out, f)
		}
	}
	return out
}

// hidden describes the codecs that were left out of a selector.
func hidden(all, shown []youtube.Format) string {
	var labels []string
	for _, f := range all {
		if !slices.ContainsFunc(shown, func(s youtube.Format) bool { return s.Itag == f.Itag }) && !slices.Contains(labels, f.CodecLabel()) {
			labels = append(labels, f.CodecLabel())
		}
	}
	return strings.Join(labels, "/")
}

// parseItags splits --itag into the requested video and audio itags (0 = not set).
func parseItags(s string) (video, audio int, err error) {
	if s == "" {
		return 0, 0, nil
	}
	parts := strings.Split(s, "+")
	if audioOnly && len(parts) == 1 {
		_, err = fmt.Sscanf(parts[0], "%d", &audio)
	} else if !audioOnly && len(parts) == 2 {
		if _, err = fmt.Sscanf(parts[0], "%d", &video); err == nil {
			_, err = fmt.Sscanf(parts[1], "%d", &audio)
		}
	} else {
		err = errors.New("want VIDEO+AUDIO")
	}
	if err != nil {
		return 0, 0, fmt.Errorf("invalid --itag %q: %v (use VIDEO+AUDIO, e.g. 299+140, or AUDIO with --audio-only)", s, err)
	}
	return video, audio, nil
}

// pickItag returns the format with the given itag from the compatible list.
func pickItag(all, formats []youtube.Format, itag int, container string) (youtube.Format, error) {
	for _, f := range formats {
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

func chooseVideo(v *youtube.Video, container string, transcode bool, itag int) (youtube.Format, error) {
	all := v.VideoFormats()
	formats := compatible(all, container, transcode)
	if itag != 0 {
		f, err := pickItag(all, formats, itag, container)
		if err == nil {
			out.Picked("Video quality", videoRow(f))
		}
		return f, err
	}
	if len(formats) == 0 {
		return youtube.Format{}, fmt.Errorf("no video streams can be saved as %s; try --format mkv", container)
	}
	if pickBest {
		out.Picked("Video quality", videoRow(formats[0]))
		return formats[0], nil
	}
	rows := make([]string, len(formats))
	for i, f := range formats {
		rows[i] = videoRow(f)
	}
	header := fmt.Sprintf("%-14s %-6s %10s %10s  %4s", "QUALITY", "CODEC", "BITRATE", "SIZE", "ITAG")
	note := ""
	if h := hidden(all, formats); h != "" {
		alt := "webm or mkv"
		if container != "mp4" {
			alt = "mkv"
		}
		note = fmt.Sprintf("%s streams do not fit in %s without re-encoding; use --format %s to see them", h, container, alt)
	}
	i, err := out.Select("Video quality", note, header, rows)
	if err != nil {
		return youtube.Format{}, err
	}
	return formats[i], nil
}

func chooseAudio(v *youtube.Video, container string, transcode bool, itag int) (youtube.Format, error) {
	all := v.AudioFormats()
	formats := compatible(all, container, transcode && !forceH264)
	if itag != 0 {
		f, err := pickItag(all, formats, itag, container)
		if err == nil {
			out.Picked("Audio quality", audioRow(f))
		}
		return f, err
	}
	if len(formats) == 0 {
		return youtube.Format{}, fmt.Errorf("no audio streams can be saved as %s", container)
	}
	if pickBest {
		out.Picked("Audio quality", audioRow(formats[0]))
		return formats[0], nil
	}
	rows := make([]string, len(formats))
	for i, f := range formats {
		rows[i] = audioRow(f)
	}
	header := fmt.Sprintf("%-6s %10s %-8s %10s  %4s  %s", "CODEC", "BITRATE", "CHANNELS", "SIZE", "ITAG", "LANGUAGE")
	i, err := out.Select("Audio quality", "", header, rows)
	if err != nil {
		return youtube.Format{}, err
	}
	return formats[i], nil
}

func videoRow(f youtube.Format) string {
	return fmt.Sprintf("%-14s %-6s %10s %10s  %4d",
		f.Resolution(), f.CodecLabel(), fmt.Sprintf("%d kbps", f.Bitrate/1000), utils.FormatSize(f.Size), f.Itag)
}

func audioRow(f youtube.Format) string {
	return fmt.Sprintf("%-6s %10s %-8s %10s  %4d  %s",
		f.CodecLabel(), fmt.Sprintf("%d kbps", f.Bitrate/1000), channels(f.Channels), utils.FormatSize(f.Size), f.Itag, f.Language)
}

func channels(n int) string {
	switch n {
	case 0:
		return "-"
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	}
	return fmt.Sprintf("%dch", n)
}
