package ffmpeg

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestArgs(t *testing.T) {
	H264Encoder = "libx264"
	cases := []struct {
		job  Job
		want []string // must appear in order-insensitive form
	}{
		{Job{AudioPath: "a", AudioCodec: "aac", Container: "mp3", Output: "o.mp3"}, []string{"-c:a libmp3lame", "-vn", "-f mp3"}},
		{Job{VideoPath: "v", VideoCodec: "vp9", AudioPath: "a", AudioCodec: "aac", Container: "mp4", ForceH264: true, Output: "o.mp4"},
			[]string{"-c:v libx264", "-c:a copy", "-movflags +faststart"}},
		{Job{VideoPath: "v", VideoCodec: "av1", AudioPath: "a", AudioCodec: "opus", Container: "mp4", ForceH264: true, Output: "o.mp4"},
			[]string{"-c:v libx264", "-c:a aac"}},
		{Job{VideoPath: "v", VideoCodec: "h264", AudioPath: "a", AudioCodec: "aac", Container: "mkv", ForceH264: true, Output: "o.mkv"},
			[]string{"-c:v copy", "-c:a copy", "-f matroska"}},
	}
	for _, c := range cases {
		args, err := Args(c.job)
		if err != nil {
			t.Fatalf("%+v: %v", c.job, err)
		}
		joined := strings.Join(args, " ")
		for _, w := range c.want {
			if !strings.Contains(joined, w) {
				t.Errorf("%s: missing %q in %s", c.job.Container, w, joined)
			}
		}
		if args[len(args)-1] != c.job.Output {
			t.Errorf("output must be last: %v", args)
		}
	}

	if _, err := Args(Job{VideoPath: "v", VideoCodec: "h264", AudioPath: "a", Container: "webm"}); err == nil {
		t.Error("H.264 into webm should fail")
	}
	if args, _ := Args(Job{AudioPath: "a", AudioCodec: "aac", Container: "m4a", Title: "x", Output: "o"}); !slices.Contains(args, "title=x") {
		t.Error("missing title metadata")
	}
}

func TestArgsReportProgressOnStdout(t *testing.T) {
	args, err := Args(Job{AudioPath: "a", AudioCodec: "aac", Container: "mp3", Output: "o.mp3"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-progress pipe:1") || !strings.Contains(joined, "-nostats") {
		t.Errorf("want -progress pipe:1 and -nostats in %s", joined)
	}
}

func TestProgressTime(t *testing.T) {
	cases := []struct {
		line string
		want time.Duration
		ok   bool
	}{
		{"out_time_us=83450000", 83450 * time.Millisecond, true},
		{"out_time_us=N/A", 0, false},
		{"out_time_ms=83450000", 0, false},
		{"frame=120", 0, false},
	}
	for _, c := range cases {
		got, ok := progressTime(c.line)
		if got != c.want || ok != c.ok {
			t.Errorf("progressTime(%q) = %v, %v; want %v, %v", c.line, got, ok, c.want, c.ok)
		}
	}
}
