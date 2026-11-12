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

	"github.com/Sn0wye/ytd/pkg/downloader"
	"github.com/Sn0wye/ytd/pkg/ui"
	"github.com/Sn0wye/ytd/pkg/utils"
	"github.com/Sn0wye/ytd/pkg/youtube"
	"github.com/spf13/cobra"
)

var (
	outputDir     string
	tmpDir        string
	fileName      string
	format        string
	audioOnly     bool
	pickBest      bool
	itags         string
	forceH264     bool
	connections   int
	playlist      bool
	noPlaylistDir bool
	items         string
	out           = ui.New()
)

var downCmd = &cobra.Command{
	Use:   "down <url>",
	Short: "Download a YouTube video or playlist, choosing video and audio quality",
	Long: `Download a YouTube video. You pick the video quality and the audio quality,
ytd downloads both streams in parallel and merges them into a single file in
the chosen format (mp4 by default). Streams are copied as-is, never
re-encoded, and no external program is needed.

Only mp3 output and --h264 need ffmpeg, because they re-encode.

A playlist URL (or --playlist with a watch URL that has list=) downloads
every video of the playlist with the best quality for the chosen format,
into a folder named after the playlist. Videos already saved there are
skipped, so running the same command again resumes the playlist.

Examples:
  ytd down https://www.youtube.com/watch?v=aqz-KE-bpKQ
  ytd down https://youtu.be/aqz-KE-bpKQ --best
  ytd down <url> --format webm
  ytd down <url> --audio-only            # m4a
  ytd down <url> --format opus
  ytd down "https://www.youtube.com/playlist?list=PL..." --format mp3
  ytd down "https://www.youtube.com/watch?v=ID&list=PL..." --playlist --items 1-10`,
	Args: cobra.ExactArgs(1),
	Run: func(_ *cobra.Command, args []string) {
		err := download(args[0])
		if errors.Is(err, errSomeFailed) {
			os.Exit(1) // the summary was already printed
		}
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
	f.StringVar(&format, "format", "", "output format: "+strings.Join(downloader.VideoContainers, ", ")+
		" (video) or "+strings.Join(downloader.AudioContainers, ", ")+" (audio only); default mp4, or m4a with --audio-only")
	f.BoolVarP(&audioOnly, "audio-only", "a", false, "download only the audio")
	f.BoolVarP(&pickBest, "best", "b", false, "skip the prompts and pick the best quality")
	f.StringVarP(&itags, "itag", "i", "", "skip the prompts and pick streams by itag: VIDEO+AUDIO, or AUDIO with --audio-only")
	f.BoolVar(&forceH264, "h264", false, "re-encode VP9/AV1 video to H.264 so it plays everywhere (needs ffmpeg, slower)")
	f.IntVarP(&connections, "connections", "c", 8, "parallel connections per download")
	f.StringVarP(&tmpDir, "tmp-dir", "t", os.TempDir(), "directory for temporary files")
	f.BoolVar(&playlist, "playlist", false, "download the whole playlist of a watch URL that has list=")
	f.BoolVar(&noPlaylistDir, "no-playlist-dir", false, "save playlist videos directly in --output-dir instead of a sub-folder")
	f.StringVar(&items, "items", "", "playlist positions to download, e.g. 1-10,15 (default: all)")
	rootCmd.AddCommand(downCmd)
}

func download(url string) error {
	start := time.Now()
	// Preserve single-video validation order, including its error output.
	if !playlist && !youtube.IsPlaylistPage(url) {
		if _, err := youtube.ExtractVideoID(url); err != nil {
			return err
		}
	}
	container, err := downloader.ResolveContainer(format, fileName, audioOnly, forceH264)
	if err != nil {
		return err
	}
	audioOnly = slices.Contains(downloader.AudioContainers, container)

	if playlist || youtube.IsPlaylistPage(url) {
		listID, err := youtube.ExtractPlaylistID(url)
		if err != nil {
			if playlist {
				return fmt.Errorf("--playlist needs a URL with a list= parameter: %w", err)
			}
			return err
		}
		return downloadPlaylist(listID, container)
	}
	if items != "" || noPlaylistDir {
		return errors.New("--items and --no-playlist-dir only apply to playlists (add --playlist to download the list of a watch URL)")
	}

	id, err := youtube.ExtractVideoID(url)
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

	opts := options(container, outputDir)
	wantVideo, wantAudio, err := parseItags(itags)
	if err != nil {
		return err
	}
	var vf *youtube.Format
	if !audioOnly {
		v, err := chooseVideo(video, opts, wantVideo)
		if err != nil {
			return err
		}
		vf = &v
	}
	af, err := chooseAudio(video, opts, wantAudio)
	if err != nil {
		return err
	}

	output, err := downloader.Run(ctx, client, video, vf, af, opts, &trailReporter{container: container})
	if err != nil {
		return err
	}

	var size int64
	if st, err := os.Stat(output); err == nil {
		size = st.Size()
	}
	out.Saved(output, size, time.Since(start))
	return nil
}

func options(container, dir string) downloader.Options {
	return downloader.Options{
		Container: container,
		ForceH264: forceH264,
		OutputDir: dir,
		TmpDir:    tmpDir,
		FileName:  fileName,
	}
}

// trailReporter shows each stage of a single download as its own step.
type trailReporter struct {
	container string
	bar       *ui.Progress
	total     int64
	stopTask  func(error)
}

func (r *trailReporter) Begin(s downloader.Stage) {
	switch s {
	case downloader.StageDownload:
		r.bar = out.StartProgress("Downloading")
	case downloader.StageTranscode:
		r.bar = out.StartProgress("Re-encoding with ffmpeg")
	case downloader.StageMux:
		r.stopTask = out.Task("Muxing into "+r.container, "Muxed into "+r.container)
	}
}

func (r *trailReporter) Bytes(done, total int64) {
	r.total = total
	r.bar.Bytes(done, total)
}

func (r *trailReporter) Media(done, total time.Duration) { r.bar.Media(done, total) }

func (r *trailReporter) End(s downloader.Stage, err error) {
	switch {
	case s == downloader.StageMux:
		r.stopTask(err)
	case err != nil:
		r.bar.Fail()
	case s == downloader.StageDownload:
		r.bar.Done("Downloaded", fmt.Sprintf("%s in %.1fs", utils.FormatSize(r.total), r.bar.Elapsed().Seconds()))
	case s == downloader.StageTranscode:
		r.bar.Done("Re-encoded", fmt.Sprintf("in %.1fs", r.bar.Elapsed().Seconds()))
	}
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

func chooseVideo(v *youtube.Video, opts downloader.Options, itag int) (youtube.Format, error) {
	container := opts.Container
	all, formats := downloader.VideoChoices(v, opts)
	if itag != 0 {
		f, err := downloader.PickItag(all, formats, itag, container)
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
	if h := downloader.Hidden(all, formats); h != "" {
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

func chooseAudio(v *youtube.Video, opts downloader.Options, itag int) (youtube.Format, error) {
	container := opts.Container
	all, formats := downloader.AudioChoices(v, opts)
	if itag != 0 {
		f, err := downloader.PickItag(all, formats, itag, container)
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
