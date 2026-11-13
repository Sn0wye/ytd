package gui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Sn0wye/ytd/pkg/downloader"
	"github.com/Sn0wye/ytd/pkg/youtube"
)

// testServer runs the handler on a real loopback listener so Host checks see
// the same port as the server.
func testServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := New(Config{Version: "test", OutputDir: t.TempDir(), TmpDir: t.TempDir()})
	s.getVideo = func(_ context.Context, id string) (*youtube.Video, error) {
		return &youtube.Video{ID: id, Title: "Big Buck Bunny", Author: "Blender", Duration: 634 * time.Second, Formats: []youtube.Format{
			{Itag: 137, Video: true, Codec: "h264", Ext: "mp4", Width: 1920, Height: 1080, Bitrate: 4_000_000, Size: 300 << 20},
			{Itag: 248, Video: true, Codec: "vp9", Ext: "webm", Width: 1920, Height: 1080, Bitrate: 3_000_000, Size: 200 << 20},
			{Itag: 140, Codec: "aac", Ext: "m4a", Bitrate: 128_000, Size: 9 << 20},
			{Itag: 251, Codec: "opus", Ext: "webm", Bitrate: 140_000, Size: 9 << 20},
		}}, nil
	}
	s.getPlaylist = func(_ context.Context, id string) (*youtube.Playlist, error) {
		return &youtube.Playlist{ID: id, Title: "My List", Skipped: 1, Entries: []youtube.Entry{
			{ID: "aqz-KE-bpKQ", Title: "One", Duration: time.Minute},
			{ID: "LYsaKn8FRhc", Title: "Two", Duration: 2 * time.Minute},
		}}, nil
	}
	s.checkFFmpeg = func() error { return nil }
	s.fetch = func(ctx context.Context, j *Job, r downloader.Reporter) (string, error) {
		r.Begin(downloader.StageDownload)
		r.Bytes(50, 100)
		r.Bytes(100, 100)
		r.End(downloader.StageDownload, nil)
		if j.VideoID == "LYsaKn8FRhc" {
			return "", errors.New("video unavailable: boom")
		}
		p := filepath.Join(j.opts.OutputDir, j.VideoID+"."+j.opts.Container)
		return p, os.WriteFile(p, []byte("data"), 0o644)
	}

	ln, _, err := s.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s, "http://" + ln.Addr().String()
}

func call(t *testing.T, s *Server, base, path string, body any, mutate ...func(*http.Request)) (int, map[string]any) {
	t.Helper()
	method, payload := http.MethodGet, ""
	if body != nil {
		b, _ := json.Marshal(body)
		method, payload = http.MethodPost, string(b)
	}
	req, _ := http.NewRequest(method, base+path, strings.NewReader(payload))
	req.Header.Set("X-YTD-Token", s.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, m := range mutate {
		m(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestGuard(t *testing.T) {
	s, base := testServer(t)

	if code, _ := call(t, s, base, "/api/state", nil); code != 200 {
		t.Errorf("valid request: %d", code)
	}
	if code, _ := call(t, s, base, "/api/state", nil, func(r *http.Request) { r.Header.Del("X-YTD-Token") }); code != 401 {
		t.Errorf("no token: %d", code)
	}
	if code, _ := call(t, s, base, "/api/state", nil, func(r *http.Request) { r.Header.Set("X-YTD-Token", "nope") }); code != 401 {
		t.Errorf("wrong token: %d", code)
	}
	if code, _ := call(t, s, base, "/api/state?token="+s.token, nil, func(r *http.Request) { r.Header.Del("X-YTD-Token") }); code != 200 {
		t.Errorf("token in query: %d", code)
	}
	// DNS rebinding: the browser sends the attacker's host name.
	if code, _ := call(t, s, base, "/api/state", nil, func(r *http.Request) { r.Host = "evil.example:1234" }); code != 403 {
		t.Errorf("foreign host: %d", code)
	}
	if code, _ := call(t, s, base, "/api/info", map[string]any{"url": "x"}, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }); code != 403 {
		t.Errorf("foreign origin: %d", code)
	}
	if code, _ := call(t, s, base, "/api/info", map[string]any{"url": "x"}, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }); code != 415 {
		t.Errorf("form post: %d", code)
	}

	// The page itself needs no token but gets the CSP.
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("index: %d, CSP %q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
}

func TestInfo(t *testing.T) {
	s, base := testServer(t)

	code, v := call(t, s, base, "/api/info", map[string]any{"url": "https://youtu.be/aqz-KE-bpKQ"})
	if code != 200 || v["kind"] != "video" || v["title"] != "Big Buck Bunny" || v["inList"] != false {
		t.Fatalf("video: %d %v", code, v)
	}
	video := v["video"].([]any)
	first := video[0].(map[string]any)
	if first["itag"].(float64) != 137 || !strings.Contains(first["label"].(string), "1080p") {
		t.Errorf("first video format: %v", first)
	}
	fits := first["fits"].([]any)
	if len(fits) != 2 || fits[0] != "mp4" || fits[1] != "mkv" {
		t.Errorf("h264 fits: %v", fits)
	}

	code, v = call(t, s, base, "/api/info", map[string]any{"url": "https://www.youtube.com/watch?v=aqz-KE-bpKQ&list=PL_Q15fKxrBb4pxTp2zQm6zZU3-h4dlm05"})
	if code != 200 || v["kind"] != "video" || v["inList"] != true {
		t.Errorf("video in list: %d %v", code, v)
	}
	code, p := call(t, s, base, "/api/info", map[string]any{"url": "https://www.youtube.com/watch?v=aqz-KE-bpKQ&list=PL_Q15fKxrBb4pxTp2zQm6zZU3-h4dlm05", "playlist": true})
	if code != 200 || p["kind"] != "playlist" || len(p["entries"].([]any)) != 2 || p["skipped"].(float64) != 1 {
		t.Errorf("playlist: %d %v", code, p)
	}

	if code, e := call(t, s, base, "/api/info", map[string]any{"url": "https://example.com"}); code != 400 || e["error"] == "" {
		t.Errorf("bad url: %d %v", code, e)
	}
}

// events reads the SSE stream into a channel of (name, data).
func events(t *testing.T, s *Server, base string) <-chan [2]string {
	t.Helper()
	resp, err := http.Get(base + "/api/events?token=" + s.token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	ch := make(chan [2]string, 100)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ch <- [2]string{name, strings.TrimPrefix(line, "data: ")}
			}
		}
		close(ch)
	}()
	return ch
}

func TestDownloadEvents(t *testing.T) {
	s, base := testServer(t)
	ev := events(t, s, base)
	if first := <-ev; first[0] != "reset" {
		t.Fatalf("first event = %v", first)
	}

	dir := s.cfg.OutputDir
	code, res := call(t, s, base, "/api/download", map[string]any{
		"items":     []map[string]string{{"id": "aqz-KE-bpKQ", "title": "One"}, {"id": "LYsaKn8FRhc", "title": "Two"}},
		"format":    "mp3",
		"outputDir": dir,
		"folder":    "My List!",
	})
	if code != 200 || len(res["jobs"].([]any)) != 2 {
		t.Fatalf("download: %d %v", code, res)
	}
	if want := filepath.Join(dir, "my-list"); res["folder"] != want {
		t.Errorf("folder = %v, want %v", res["folder"], want)
	}

	final := map[string]Job{}
	sawProgress := false
	timeout := time.After(5 * time.Second)
	for len(final) < 2 {
		select {
		case e := <-ev:
			var j Job
			json.Unmarshal([]byte(e[1]), &j)
			if j.Status == statusDownloading && j.Progress == 0.5 {
				sawProgress = true
			}
			if j.Status == statusSaved || j.Status == statusFailed {
				final[j.VideoID] = j
			}
		case <-timeout:
			t.Fatalf("timed out; got %v", final)
		}
	}
	if !sawProgress {
		t.Error("no progress event at 50%")
	}
	if j := final["aqz-KE-bpKQ"]; j.Status != statusSaved || filepath.Base(j.Path) != "aqz-KE-bpKQ.mp3" {
		t.Errorf("saved job: %+v", j)
	}
	if j := final["LYsaKn8FRhc"]; j.Status != statusFailed || !strings.Contains(j.Error, "boom") {
		t.Errorf("failed job: %+v", j)
	}

	if code, _ := call(t, s, base, "/api/clear", map[string]any{}); code != 200 {
		t.Errorf("clear: %d", code)
	}
	if _, st := call(t, s, base, "/api/state", nil); len(st["jobs"].([]any)) != 0 {
		t.Errorf("jobs after clear: %v", st["jobs"])
	}
}

func TestDownloadValidation(t *testing.T) {
	s, base := testServer(t)
	one := []map[string]string{{"id": "aqz-KE-bpKQ", "title": "One"}}
	two := append(one, map[string]string{"id": "LYsaKn8FRhc", "title": "Two"})
	cases := []map[string]any{
		{"items": []any{}, "format": "mp4", "outputDir": s.cfg.OutputDir},
		{"items": one, "format": "avi", "outputDir": s.cfg.OutputDir},
		{"items": one, "format": "mp3", "h264": true, "outputDir": s.cfg.OutputDir},
		{"items": one, "format": "mp4", "outputDir": "relative/dir"},
		{"items": two, "format": "mp4", "videoItag": 137, "outputDir": s.cfg.OutputDir},
		{"items": []map[string]string{{"id": "../../etc", "title": "x"}}, "format": "mp4", "outputDir": s.cfg.OutputDir},
	}
	for _, c := range cases {
		if code, res := call(t, s, base, "/api/download", c); code != 400 {
			t.Errorf("%v: %d %v", c, code, res)
		}
	}

	s.checkFFmpeg = func() error { return errors.New("missing") }
	code, res := call(t, s, base, "/api/download", map[string]any{"items": one, "format": "mp3", "outputDir": s.cfg.OutputDir})
	if code != 400 || !strings.Contains(res["error"].(string), "ffmpeg") {
		t.Errorf("mp3 without ffmpeg: %d %v", code, res)
	}
}

func TestCancelQueued(t *testing.T) {
	s, base := testServer(t)
	release := make(chan struct{})
	s.fetch = func(ctx context.Context, j *Job, r downloader.Reporter) (string, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "", errors.New("unexpected")
	}
	items := []map[string]string{{"id": "aqz-KE-bpKQ", "title": "One"}, {"id": "LYsaKn8FRhc", "title": "Two"}}
	if code, _ := call(t, s, base, "/api/download", map[string]any{"items": items, "format": "mp4", "outputDir": s.cfg.OutputDir}); code != 200 {
		t.Fatal(code)
	}
	if code, _ := call(t, s, base, "/api/cancel", map[string]any{"id": ""}); code != 200 {
		t.Fatal(code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, st := call(t, s, base, "/api/state", nil)
		all := true
		for _, j := range st["jobs"].([]any) {
			if j.(map[string]any)["status"] != statusCancelled {
				all = false
			}
		}
		if all {
			close(release)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
	t.Error("jobs were not all cancelled")
}

func TestIdleShutdown(t *testing.T) {
	s := New(Config{OutputDir: t.TempDir(), IdleTimeout: 50 * time.Millisecond})
	ln, _, err := s.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), ln) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop when idle")
	}
}

func TestStateSnapshot(t *testing.T) {
	s := New(Config{})
	s.jobs = []*Job{{ID: "1", Status: statusQueued}}
	state, err := s.handleState(nil)
	if err != nil {
		t.Fatal(err)
	}
	s.update(s.jobs[0], func(j *Job) { j.Status = statusSaved })
	jobs := state.(map[string]any)["jobs"].([]Job)
	if jobs[0].Status != statusQueued {
		t.Fatal("state aliases mutable jobs")
	}
}

func TestQueueFull(t *testing.T) {
	s := New(Config{OutputDir: t.TempDir()})
	s.queue = make(chan *Job, 1)
	s.queue <- &Job{}
	body := `{"items":[{"id":"aqz-KE-bpKQ"}],"format":"mp4","outputDir":` + strconv.Quote(s.cfg.OutputDir) + `}`
	req, _ := http.NewRequest(http.MethodPost, "/api/download", strings.NewReader(body))
	if _, err := s.handleDownload(req); err == nil {
		t.Fatal("accepted a full queue")
	}
	if len(s.jobs) != 0 {
		t.Fatal("partially published jobs")
	}
}

func TestSlowSubscriberResets(t *testing.T) {
	s := New(Config{})
	ch := make(chan []byte, 1)
	s.subs[ch] = struct{}{}
	s.jobs = []*Job{{ID: "1", Status: statusQueued}, {ID: "2", Status: statusQueued}}
	s.publishLocked(s.jobs[0])
	s.jobs[0].Status = statusSaved
	s.publishLocked(s.jobs[0])
	msg := string(<-ch)
	if !strings.Contains(msg, "event: reset") || !strings.Contains(msg, `"status":"saved"`) || !strings.Contains(msg, `"id":"2"`) {
		t.Fatal(msg)
	}
}
