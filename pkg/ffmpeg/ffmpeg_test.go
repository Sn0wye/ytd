package ffmpeg

import (
	"slices"
	"strings"
	"testing"
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
