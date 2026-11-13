// Package gui serves ytd's graphical interface: a small web app on the
// loopback interface, opened in the default browser. It uses the same
// pipeline as the terminal command (pkg/downloader).
//
// Only the local user should be able to drive it: the server listens on
// 127.0.0.1, every API call needs the random token that is part of the URL
// opened in the browser, and requests with a foreign Host or Origin header
// are rejected (other websites, DNS rebinding).
package gui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sn0wye/ytd/pkg/downloader"
	"github.com/Sn0wye/ytd/pkg/ffmpeg"
	"github.com/Sn0wye/ytd/pkg/utils"
	"github.com/Sn0wye/ytd/pkg/youtube"
)

//go:embed static
var staticFiles embed.FS

// Config sets up a Server.
type Config struct {
	Version     string
	OutputDir   string // default folder offered in the page
	TmpDir      string
	Connections int
	// IdleTimeout stops the server when no page has been connected for this
	// long and nothing is downloading. 0 disables it.
	IdleTimeout time.Duration
}

// Server is the GUI backend. Create it with New.
type Server struct {
	cfg   Config
	token string
	port  int

	// Replaceable in tests.
	getVideo    func(ctx context.Context, id string) (*youtube.Video, error)
	getPlaylist func(ctx context.Context, id string) (*youtube.Playlist, error)
	fetch       func(ctx context.Context, j *Job, r downloader.Reporter) (string, error)
	checkFFmpeg func() error

	mu       sync.Mutex
	jobs     []*Job
	queue    chan *Job
	subs     map[chan []byte]struct{}
	lastSeen time.Time
	nextID   int

	stop context.CancelFunc
}

// Job is one video in the download queue.
type Job struct {
	ID       string  `json:"id"`
	VideoID  string  `json:"videoId"`
	Title    string  `json:"title"`
	Group    string  `json:"group,omitempty"` // playlist title
	Format   string  `json:"format"`
	Status   string  `json:"status"` // queued, downloading, converting, saving, saved, failed, cancelled
	Progress float64 `json:"progress"`
	Detail   string  `json:"detail,omitempty"` // speed and time left, or size when saved
	Error    string  `json:"error,omitempty"`
	Path     string  `json:"path,omitempty"`

	opts   downloader.Options
	sel    downloader.Selection
	cancel context.CancelFunc
	start  time.Time
}

const (
	statusQueued      = "queued"
	statusDownloading = "downloading"
	statusConverting  = "converting"
	statusSaving      = "saving"
	statusSaved       = "saved"
	statusFailed      = "failed"
	statusCancelled   = "cancelled"
)

func (j *Job) finished() bool {
	return j.Status == statusSaved || j.Status == statusFailed || j.Status == statusCancelled
}

// New returns a server with a fresh token.
func New(cfg Config) *Server {
	if cfg.Connections < 1 {
		cfg.Connections = 8
	}
	if cfg.TmpDir == "" {
		cfg.TmpDir = os.TempDir()
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	client := youtube.NewClient(cfg.Connections)
	s := &Server{
		cfg:         cfg,
		token:       hex.EncodeToString(b),
		getVideo:    client.GetVideo,
		getPlaylist: client.GetPlaylist,
		checkFFmpeg: ffmpeg.CheckInstalled,
		queue:       make(chan *Job, 10000),
		subs:        map[chan []byte]struct{}{},
		lastSeen:    time.Now(),
	}
	s.fetch = func(ctx context.Context, j *Job, r downloader.Reporter) (string, error) {
		path, _, err := downloader.Fetch(ctx, client, j.VideoID, j.opts, j.sel, r, nil)
		return path, err
	}
	return s
}

// Listen binds a loopback port (0 picks a free one) and returns the URL to
// open, which carries the token.
func (s *Server) Listen(port int) (net.Listener, string, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, "", err
	}
	s.port = ln.Addr().(*net.TCPAddr).Port
	return ln, fmt.Sprintf("http://127.0.0.1:%d/?token=%s", s.port, s.token), nil
}

// Serve runs until ctx is cancelled, the page asks to quit, or the server has
// been idle for cfg.IdleTimeout.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.stop = cancel

	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		s.worker(ctx)
	}()
	if s.cfg.IdleTimeout > 0 {
		go s.watchIdle(ctx)
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	var serveErr error
	select {
	case serveErr = <-errc:
		cancel()
	case <-ctx.Done():
	}
	s.cancelAll()
	shutdownCtx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	srv.Shutdown(shutdownCtx)
	srv.Close()  // close SSE connections still waiting for events
	<-workerDone // Run must finish removing its temporary directory
	return serveErr
}

// Handler returns the HTTP handler: static files and the JSON API.
func (s *Server) Handler() http.Handler {
	static, _ := fs.Sub(staticFiles, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/state", s.api(s.handleState))
	mux.HandleFunc("POST /api/info", s.api(s.handleInfo))
	mux.HandleFunc("POST /api/download", s.api(s.handleDownload))
	mux.HandleFunc("POST /api/cancel", s.api(s.handleCancel))
	mux.HandleFunc("POST /api/clear", s.api(s.handleClear))
	mux.HandleFunc("POST /api/open", s.api(s.handleOpen))
	mux.HandleFunc("POST /api/quit", s.api(s.handleQuit))
	mux.HandleFunc("GET /api/events", s.handleEvents)
	return s.guard(mux)
}

// guard rejects requests that do not come from the page this server opened.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowedHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !s.allowedOrigin(o) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !s.validToken(r) {
			http.Error(w, "missing or wrong token", http.StatusUnauthorized)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https://i.ytimg.com; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowedHost(host string) bool {
	p := strconv.Itoa(s.port)
	return host == "127.0.0.1:"+p || host == "localhost:"+p
}

func (s *Server) allowedOrigin(origin string) bool {
	p := strconv.Itoa(s.port)
	return origin == "http://127.0.0.1:"+p || origin == "http://localhost:"+p
}

func (s *Server) validToken(r *http.Request) bool {
	t := r.Header.Get("X-YTD-Token")
	if t == "" {
		t = r.URL.Query().Get("token") // EventSource cannot set headers
	}
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func badRequest(format string, a ...any) error {
	return &apiError{http.StatusBadRequest, fmt.Sprintf(format, a...)}
}

// api decodes the JSON body into the handler's input and encodes its result.
func (s *Server) api(h func(r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "want application/json", http.StatusUnsupportedMediaType)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		res, err := h(r)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			status := http.StatusBadGateway // YouTube errors
			var ae *apiError
			if errors.As(err, &ae) {
				status = ae.status
			}
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(res)
	}
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return badRequest("invalid request: %v", err)
	}
	return nil
}

func (s *Server) handleState(_ *http.Request) (any, error) {
	s.mu.Lock()
	jobs := make([]Job, len(s.jobs))
	for i, j := range s.jobs {
		jobs[i] = *j
	}
	s.mu.Unlock()
	return map[string]any{
		"version":         s.cfg.Version,
		"outputDir":       s.cfg.OutputDir,
		"ffmpeg":          s.checkFFmpeg() == nil,
		"ffmpegHint":      ffmpeg.InstallHint(runtime.GOOS),
		"videoContainers": downloader.VideoContainers,
		"audioContainers": downloader.AudioContainers,
		"jobs":            jobs,
	}, nil
}

type formatInfo struct {
	Itag  int      `json:"itag"`
	Label string   `json:"label"`
	Fits  []string `json:"fits"` // containers it can go into without re-encoding
}

func formatList(list []youtube.Format, video bool) []formatInfo {
	containers := downloader.AudioContainers
	if video {
		containers = downloader.VideoContainers
	}
	out := make([]formatInfo, 0, len(list))
	for _, f := range list {
		fi := formatInfo{Itag: f.Itag, Fits: []string{}}
		for _, c := range containers {
			if downloader.Fits(f, c) {
				fi.Fits = append(fi.Fits, c)
			}
		}
		parts := []string{}
		if video {
			parts = append(parts, f.Resolution())
		}
		parts = append(parts, f.CodecLabel(), fmt.Sprintf("%d kbps", f.Bitrate/1000), utils.FormatSize(f.Size))
		if f.Language != "" {
			parts = append(parts, f.Language)
		}
		fi.Label = strings.Join(parts, " · ")
		out = append(out, fi)
	}
	return out
}

func (s *Server) handleInfo(r *http.Request) (any, error) {
	var in struct {
		URL      string `json:"url"`
		Playlist bool   `json:"playlist"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	in.URL = strings.TrimSpace(in.URL)
	if in.URL == "" {
		return nil, badRequest("paste a YouTube link first")
	}

	if in.Playlist || youtube.IsPlaylistPage(in.URL) {
		listID, err := youtube.ExtractPlaylistID(in.URL)
		if err != nil {
			return nil, badRequest("%v", err)
		}
		p, err := s.getPlaylist(r.Context(), listID)
		if err != nil {
			return nil, err
		}
		type entry struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Duration int    `json:"duration"`
		}
		entries := make([]entry, len(p.Entries))
		for i, e := range p.Entries {
			entries[i] = entry{e.ID, e.Title, int(e.Duration.Seconds())}
		}
		return map[string]any{"kind": "playlist", "id": p.ID, "title": p.Title, "author": p.Author, "skipped": p.Skipped, "entries": entries}, nil
	}

	id, err := youtube.ExtractVideoID(in.URL)
	if err != nil {
		return nil, badRequest("%v", err)
	}
	v, err := s.getVideo(r.Context(), id)
	if err != nil {
		return nil, err
	}
	_, hasList := youtube.ExtractPlaylistID(in.URL)
	return map[string]any{
		"kind":      "video",
		"id":        v.ID,
		"title":     v.Title,
		"author":    v.Author,
		"duration":  int(v.Duration.Seconds()),
		"thumbnail": "https://i.ytimg.com/vi/" + v.ID + "/mqdefault.jpg",
		"video":     formatList(v.VideoFormats(), true),
		"audio":     formatList(v.AudioFormats(), false),
		"inList":    hasList == nil,
	}, nil
}

type downloadRequest struct {
	Items []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"items"`
	Format    string `json:"format"`
	H264      bool   `json:"h264"`
	VideoItag int    `json:"videoItag"`
	AudioItag int    `json:"audioItag"`
	OutputDir string `json:"outputDir"`
	Folder    string `json:"folder"` // playlist title: save into a sub-folder
}

func (s *Server) handleDownload(r *http.Request) (any, error) {
	var in downloadRequest
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if len(in.Items) == 0 {
		return nil, badRequest("nothing selected")
	}
	container, err := downloader.ResolveContainer(in.Format, "", false, in.H264)
	if err != nil {
		return nil, badRequest("%v", strings.ReplaceAll(err.Error(), "--h264", "H.264"))
	}
	if (container == "mp3" || in.H264) && s.checkFFmpeg() != nil {
		return nil, badRequest("%s needs ffmpeg, which was not found. Install it with: %s", strings.ToUpper(container), ffmpeg.InstallHint(runtime.GOOS))
	}
	if len(in.Items) > 1 && (in.VideoItag != 0 || in.AudioItag != 0) {
		return nil, badRequest("a quality can only be picked for a single video")
	}

	dir := strings.TrimSpace(in.OutputDir)
	if strings.HasPrefix(dir, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, dir[1:])
		}
	}
	if !filepath.IsAbs(dir) {
		return nil, badRequest("the folder must be a full path, like %s", s.cfg.OutputDir)
	}
	dir = filepath.Clean(dir)
	if in.Folder != "" {
		dir = filepath.Join(dir, downloader.Name("", in.Folder, "playlist"))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, badRequest("cannot use folder %s: %v", dir, err)
	}

	opts := downloader.Options{Container: container, ForceH264: in.H264, OutputDir: dir, TmpDir: s.cfg.TmpDir}
	sel := downloader.Selection{VideoItag: in.VideoItag, AudioItag: in.AudioItag}
	if opts.AudioOnly() {
		sel.VideoItag = 0
	}

	var ids []string
	s.mu.Lock()
	// Reserve capacity before publishing any jobs; never block with mu held.
	if len(in.Items) > cap(s.queue)-len(s.queue) {
		s.mu.Unlock()
		return nil, badRequest("download queue is full")
	}
	for _, it := range in.Items {
		id, err := youtube.ExtractVideoID(it.ID)
		if err != nil {
			continue
		}
		s.nextID++
		j := &Job{
			ID: strconv.Itoa(s.nextID), VideoID: id, Title: it.Title, Group: in.Folder,
			Format: container, Status: statusQueued, opts: opts, sel: sel,
		}
		if j.Title == "" {
			j.Title = id
		}
		if downloader.Exists(dir, it.Title, id, container) && in.Folder != "" {
			j.Status, j.Progress, j.Detail = statusSaved, 1, "already downloaded"
			j.Path = filepath.Join(dir, downloader.Name("", it.Title, id)+"."+container)
		}
		s.jobs = append(s.jobs, j)
		ids = append(ids, j.ID)
		s.publishLocked(j)
		if j.Status == statusQueued {
			s.queue <- j
		}
	}
	s.mu.Unlock()
	if len(ids) == 0 {
		return nil, badRequest("no valid video IDs")
	}
	return map[string]any{"jobs": ids, "folder": dir}, nil
}

func (s *Server) jobByID(id string) *Job {
	for _, j := range s.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func (s *Server) handleCancel(r *http.Request) (any, error) {
	var in struct {
		ID string `json:"id"` // "" cancels everything
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if (in.ID == "" || j.ID == in.ID) && !j.finished() {
			s.cancelLocked(j)
		}
	}
	return map[string]bool{"ok": true}, nil
}

func (s *Server) cancelLocked(j *Job) {
	if j.cancel != nil {
		j.cancel() // the worker marks it cancelled
		return
	}
	j.Status, j.Detail = statusCancelled, ""
	s.publishLocked(j)
}

func (s *Server) cancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if !j.finished() {
			s.cancelLocked(j)
		}
	}
}

func (s *Server) handleClear(_ *http.Request) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = slices.DeleteFunc(s.jobs, (*Job).finished)
	s.broadcastLocked("reset", append([]*Job{}, s.jobs...))
	return map[string]bool{"ok": true}, nil
}

func (s *Server) handleOpen(r *http.Request) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	s.mu.Lock()
	j := s.jobByID(in.ID)
	var path string
	if j != nil {
		path = j.Path
	}
	s.mu.Unlock()
	if path == "" {
		return nil, badRequest("this download has no file yet")
	}
	if err := revealFile(path); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

func (s *Server) handleQuit(_ *http.Request) (any, error) {
	if s.stop != nil {
		go func() {
			time.Sleep(200 * time.Millisecond) // let the response go out
			s.stop()
		}()
	}
	return map[string]bool{"ok": true}, nil
}

// handleEvents streams job updates as Server-Sent Events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	ch := make(chan []byte, 256)

	s.mu.Lock()
	s.subs[ch] = struct{}{}
	snapshot := event("reset", append([]*Job{}, s.jobs...))
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.lastSeen = time.Now()
		s.mu.Unlock()
	}()

	w.Write(snapshot)
	flusher.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			w.Write(msg)
			flusher.Flush()
		case <-ping.C:
			w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

func event(name string, v any) []byte {
	b, _ := json.Marshal(v)
	return []byte("event: " + name + "\ndata: " + string(b) + "\n\n")
}

func (s *Server) broadcastLocked(name string, v any) {
	msg := event(name, v)
	for ch := range s.subs {
		select {
		case ch <- msg:
		default:
			// Resynchronize the entire queue; the dropped event may be terminal.
		drain:
			for {
				select {
				case <-ch:
				default:
					break drain
				}
			}
			ch <- event("reset", s.jobs)
		}
	}
}

func (s *Server) publishLocked(j *Job) { s.broadcastLocked("job", j) }

func (s *Server) update(j *Job, f func(j *Job)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(j)
	s.publishLocked(j)
}

// worker downloads queued jobs one at a time (YouTube rate-limits
// aggressive parallel downloads).
func (s *Server) worker(ctx context.Context) {
	for {
		var j *Job
		select {
		case <-ctx.Done():
			return
		case j = <-s.queue:
		}

		jctx, cancel := context.WithCancel(ctx)
		s.mu.Lock()
		if j.Status != statusQueued {
			s.mu.Unlock()
			cancel()
			continue
		}
		j.cancel = cancel
		j.start = time.Now()
		j.Status = statusDownloading
		s.publishLocked(j)
		job := *j // immutable input for the downloader
		s.mu.Unlock()

		path, err := s.fetch(jctx, &job, &jobReporter{s: s, j: j})
		cancelled := jctx.Err() != nil && err != nil
		cancel()

		s.update(j, func(j *Job) {
			j.cancel = nil
			switch {
			case cancelled:
				j.Status, j.Detail, j.Progress = statusCancelled, "", 0
			case err != nil:
				j.Status, j.Error, j.Detail = statusFailed, err.Error(), ""
			default:
				var size int64
				if st, err := os.Stat(path); err == nil {
					size = st.Size()
				}
				j.Status, j.Path, j.Progress = statusSaved, path, 1
				j.Detail = fmt.Sprintf("%s · %.1fs", utils.FormatSize(size), time.Since(j.start).Seconds())
			}
		})
	}
}

// busy reports whether a job is queued or running.
func (s *Server) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.ContainsFunc(s.jobs, func(j *Job) bool { return !j.finished() })
}

func (s *Server) watchIdle(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		idle := len(s.subs) == 0 && time.Since(s.lastSeen) > s.cfg.IdleTimeout
		s.mu.Unlock()
		if idle && !s.busy() {
			s.stop()
			return
		}
	}
}

// jobReporter turns downloader progress into job updates.
type jobReporter struct {
	s     *Server
	j     *Job
	start time.Time
}

func (r *jobReporter) Begin(st downloader.Stage) {
	r.start = time.Now()
	r.s.update(r.j, func(j *Job) {
		j.Progress, j.Detail = 0, ""
		switch st {
		case downloader.StageDownload:
			j.Status = statusDownloading
		case downloader.StageTranscode:
			j.Status = statusConverting
		case downloader.StageMux:
			j.Status = statusSaving
		}
	})
}

func (r *jobReporter) Bytes(done, total int64) {
	el := time.Since(r.start).Seconds()
	speed := float64(done) / max(el, 0.001)
	detail := fmt.Sprintf("%s / %s · %s/s", utils.FormatSize(done), utils.FormatSize(total), utils.FormatSize(int64(speed)))
	r.s.update(r.j, func(j *Job) {
		j.Progress = float64(done) / float64(max(total, 1))
		j.Detail = detail
	})
}

func (r *jobReporter) Media(done, total time.Duration) {
	r.s.update(r.j, func(j *Job) {
		j.Progress = done.Seconds() / max(total.Seconds(), 0.001)
		j.Detail = fmt.Sprintf("%.0f%%", 100*j.Progress)
	})
}

func (r *jobReporter) End(downloader.Stage, error) {}
