package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Sn0wye/ytd/pkg/downloader"
	"github.com/Sn0wye/ytd/pkg/ffmpeg"
	"github.com/Sn0wye/ytd/pkg/ui"
	"github.com/Sn0wye/ytd/pkg/utils"
	"github.com/Sn0wye/ytd/pkg/youtube"
)

// errSomeFailed ends a playlist run in which some videos failed. The summary
// is already printed, so the command only sets the exit code.
var errSomeFailed = errors.New("some playlist videos failed")

const maxLabel = 60

func downloadPlaylist(listID, container string) error {
	if fileName != "" {
		return errors.New("--filename cannot be used with a playlist (each video is named after its title)")
	}
	if itags != "" {
		return errors.New("--itag cannot be used with a playlist (itags differ per video); the best quality for the format is used")
	}
	start := time.Now()
	opts := options(container, outputDir)
	if container == "mp3" || forceH264 {
		// Fail before listing the playlist rather than on every video.
		if err := ffmpeg.CheckInstalled(); err != nil {
			return fmt.Errorf("%s output needs ffmpeg to re-encode: %w", container, err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := youtube.NewClient(connections)
	out.Intro(version)
	stopTask := out.Task("Fetching playlist", "Playlist found")
	p, err := client.GetPlaylist(ctx, listID)
	stopTask(err)
	if err != nil {
		return err
	}
	positions, err := downloader.ParseItems(items, len(p.Entries))
	if err != nil {
		return err
	}
	detail := fmt.Sprintf("%d videos", len(p.Entries))
	if len(positions) != len(p.Entries) {
		detail = fmt.Sprintf("%d of %d videos", len(positions), len(p.Entries))
	}
	if p.Skipped > 0 {
		detail += fmt.Sprintf(" · %d private or deleted left out", p.Skipped)
	}
	out.Describe(p.Title, detail)

	if !noPlaylistDir {
		opts.OutputDir = filepath.Join(outputDir, downloader.Name("", p.Title, p.ID))
	}
	out.Picked("Saving as "+container, "best quality · "+ui.ShortPath(opts.OutputDir))

	var saved, skipped int
	var failed []string
	for k, pos := range positions {
		e := p.Entries[pos-1]
		label := fmt.Sprintf("[%d/%d] %s", k+1, len(positions), truncate(e.Title, maxLabel))
		if downloader.Exists(opts.OutputDir, e.Title, e.ID, container) {
			out.Picked(label, "already downloaded")
			skipped++
			continue
		}

		began := time.Now()
		r := &entryReporter{label: label}
		path, _, err := downloader.Fetch(ctx, client, e.ID, opts, downloader.Selection{}, r, nil)
		if ctx.Err() != nil {
			r.fail(ctx.Err())
			return ui.ErrAborted
		}
		if err != nil {
			r.fail(err)
			out.Warn(err.Error())
			failed = append(failed, fmt.Sprintf("%s  %s", e.ID, e.Title))
			continue
		}
		var size int64
		if st, err := os.Stat(path); err == nil {
			size = st.Size()
		}
		r.bar.Done(label, fmt.Sprintf("%s · %.1fs", utils.FormatSize(size), time.Since(began).Seconds()))
		saved++
	}

	summary := fmt.Sprintf("%d saved, %d skipped, %d failed", saved, skipped, len(failed))
	details := []string{fmt.Sprintf("%s · %s", ui.ShortPath(opts.OutputDir), ui.Duration(time.Since(start)))}
	if len(failed) > 0 {
		details = append(details, "Failed:")
		details = append(details, failed...)
	}
	out.Finish(len(failed) == 0, summary, strings.Join(details, "\n"))
	if len(failed) > 0 {
		return errSomeFailed
	}
	return nil
}

// entryReporter shows a whole playlist entry (download and re-encode) as
// one progress step.
type entryReporter struct {
	label    string
	bar      *ui.Progress
	attempts int
}

func (r *entryReporter) Begin(s downloader.Stage) {
	if s != downloader.StageDownload {
		return
	}
	r.attempts++
	if r.attempts > 1 {
		out.Warn("stream URL expired, retrying with fresh URLs")
	}
	r.bar = out.StartProgress(r.label)
}

func (r *entryReporter) Bytes(done, total int64)         { r.bar.Bytes(done, total) }
func (r *entryReporter) Media(done, total time.Duration) { r.bar.Media(done, total) }

func (r *entryReporter) End(_ downloader.Stage, err error) {
	if err != nil {
		r.bar.Fail()
		r.bar = nil
	}
}

// fail marks the entry as failed if its bar has not already been closed.
func (r *entryReporter) fail(err error) {
	if r.bar != nil {
		r.bar.Fail()
		r.bar = nil
		return
	}
	if r.attempts == 0 {
		// It failed before downloading (e.g. the video is unavailable).
		out.Task(r.label, r.label)(err)
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
