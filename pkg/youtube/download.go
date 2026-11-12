package youtube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// chunkSize matches yt-dlp's http_chunk_size for YouTube. Requests for larger
// ranges get throttled by googlevideo.
const chunkSize = 10 << 20

const maxAttempts = 5

// ErrForbidden is returned when googlevideo refuses a stream URL. Fetching
// the video info again gives fresh URLs that usually work.
var ErrForbidden = errors.New("YouTube refused the download (HTTP 403); the stream URL may have expired, try again")

// Job is one stream to download into Path.
type Job struct {
	Format Format
	Path   string
}

// Progress reports downloaded and total bytes across all jobs.
type Progress func(done, total int64)

type chunk struct {
	file     *os.File
	url      string
	off, end int64 // inclusive end
}

// Download fetches all jobs concurrently, splitting each stream into ranges
// spread over c.Connections connections. progress (optional) is called from
// a single goroutine about every 200 ms and once at the end.
func (c *Client) Download(ctx context.Context, jobs []Job, progress Progress) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var total int64
	var chunks []chunk
	var files []*os.File
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()

	var wg sync.WaitGroup
	var done atomic.Int64

	for _, j := range jobs {
		f, err := os.Create(j.Path)
		if err != nil {
			return err
		}
		files = append(files, f)

		size := j.Format.Size
		if size <= 0 {
			return fmt.Errorf("stream %d has unknown size", j.Format.Itag)
		}
		if err := f.Truncate(size); err != nil {
			return err
		}
		total += size
		for off := int64(0); off < size; off += chunkSize {
			chunks = append(chunks, chunk{file: f, url: j.Format.URL, off: off, end: min(off+chunkSize, size) - 1})
		}
	}

	queue := make(chan chunk)
	for range c.Connections {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ch := range queue {
				if err := c.fetchChunk(ctx, ch, &done); err != nil {
					cancel(err)
					return
				}
			}
		}()
	}

	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		if progress == nil {
			<-stopProgress
			return
		}
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				progress(done.Load(), total)
			case <-stopProgress:
				progress(done.Load(), max(total, done.Load()))
				return
			}
		}
	}()

feed:
	for _, ch := range chunks {
		select {
		case queue <- ch:
		case <-ctx.Done():
			break feed
		}
	}
	close(queue)
	wg.Wait()
	close(stopProgress)
	<-progressDone

	if err := context.Cause(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return ctx.Err()
}

// fetchChunk downloads one range, resuming after partial reads.
func (c *Client) fetchChunk(ctx context.Context, ch chunk, done *atomic.Int64) error {
	off := ch.off
	var lastErr error
	for attempt := 0; attempt < maxAttempts && off <= ch.end; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		n, err := c.fetchRange(ctx, ch.file, ch.url, off, ch.end, done)
		off += n
		if err == nil && off > ch.end {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var se *statusError
		if errors.As(err, &se) && se.code == http.StatusForbidden {
			return ErrForbidden
		}
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		lastErr = err
	}
	return fmt.Errorf("download failed after %d attempts: %w", maxAttempts, lastErr)
}

type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

// fetchRange writes bytes [off, end] of url into f at off and returns the
// number of bytes written, even on error.
func (c *Client) fetchRange(ctx context.Context, f *os.File, url string, off, end int64, done *atomic.Int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s&range=%d-%d", url, off, end), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", visionOS.ua)
	req.Header.Set("Origin", origin)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return 0, &statusError{resp.StatusCode}
	}

	w := &countingWriter{w: io.NewOffsetWriter(f, off), done: done}
	limit := io.LimitReader(resp.Body, end-off+1)
	buf := make([]byte, 256<<10)
	_, err = io.CopyBuffer(w, limit, buf)
	return w.n, err
}

type countingWriter struct {
	w    io.Writer
	n    int64
	done *atomic.Int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n += int64(n)
	cw.done.Add(int64(n))
	return n, err
}
