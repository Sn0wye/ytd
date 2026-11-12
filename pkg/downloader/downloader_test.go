package downloader

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Sn0wye/ytd/pkg/youtube"
)

func TestParseItems(t *testing.T) {
	cases := []struct {
		spec string
		n    int
		want []int
	}{
		{"", 3, []int{1, 2, 3}},
		{"1-3,5", 10, []int{1, 2, 3, 5}},
		{"5,1-3,2", 10, []int{1, 2, 3, 5}},
		{"8-", 10, []int{8, 9, 10}},
		{"-2", 10, []int{1, 2}},
		{"3-50", 5, []int{3, 4, 5}},
		{" 2 , 4 ", 5, []int{2, 4}},
	}
	for _, c := range cases {
		got, err := ParseItems(c.spec, c.n)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("ParseItems(%q, %d) = %v, %v; want %v", c.spec, c.n, got, err, c.want)
		}
	}
	for _, bad := range []string{"0", "a", "3-1", "1-x", "20-30"} {
		if got, err := ParseItems(bad, 10); err == nil {
			t.Errorf("ParseItems(%q) = %v, want error", bad, got)
		}
	}
}

func TestResolveContainer(t *testing.T) {
	cases := []struct {
		format, file string
		audio, h264  bool
		want         string
		wantErr      bool
	}{
		{"", "", false, false, "mp4", false},
		{"", "", true, false, "m4a", false},
		{"MP3", "", false, false, "mp3", false},
		{"", "song.opus", false, false, "opus", false},
		{"mkv", "", true, false, "", true},
		{"avi", "", false, false, "", true},
		{"mp3", "", false, true, "", true},
		{"webm", "", false, true, "", true},
		{"mkv", "", false, true, "mkv", false},
	}
	for _, c := range cases {
		got, err := ResolveContainer(c.format, c.file, c.audio, c.h264)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("ResolveContainer(%q, %q, %v, %v) = %q, %v", c.format, c.file, c.audio, c.h264, got, err)
		}
	}
}

func testVideo() *youtube.Video {
	return &youtube.Video{ID: "aqz-KE-bpKQ", Title: "Big Buck Bunny", Formats: []youtube.Format{
		{Itag: 313, Video: true, Codec: "vp9", Ext: "webm", Width: 3840, Height: 2160, Bitrate: 20_000_000},
		{Itag: 137, Video: true, Codec: "h264", Ext: "mp4", Width: 1920, Height: 1080, Bitrate: 4_000_000},
		{Itag: 251, Codec: "opus", Ext: "webm", Bitrate: 140_000},
		{Itag: 140, Codec: "aac", Ext: "m4a", Bitrate: 128_000},
	}}
}

func TestSelect(t *testing.T) {
	v := testVideo()

	vf, af, err := Select(v, Options{Container: "mp4"}, Selection{})
	if err != nil || vf == nil || vf.Itag != 137 || af.Itag != 140 {
		t.Errorf("mp4 best: %v %v %v", vf, af.Itag, err)
	}
	vf, af, err = Select(v, Options{Container: "webm"}, Selection{})
	if err != nil || vf.Itag != 313 || af.Itag != 251 {
		t.Errorf("webm best: %v %v %v", vf, af.Itag, err)
	}
	// mp3 re-encodes: no video, any audio, best first.
	vf, af, err = Select(v, Options{Container: "mp3"}, Selection{})
	if err != nil || vf != nil || af.Itag != 251 {
		t.Errorf("mp3 best: %v %v %v", vf, af.Itag, err)
	}
	// --h264 re-encodes the video, so VP9 is usable, but audio must fit.
	vf, af, err = Select(v, Options{Container: "mp4", ForceH264: true}, Selection{})
	if err != nil || vf.Itag != 313 || af.Itag != 140 {
		t.Errorf("mp4 h264 best: %v %v %v", vf, af.Itag, err)
	}
	if _, _, err := Select(v, Options{Container: "mp4"}, Selection{VideoItag: 313, AudioItag: 140}); err == nil {
		t.Error("VP9 into mp4 without re-encoding: want error")
	}
	if _, _, err := Select(v, Options{Container: "m4a"}, Selection{AudioItag: 999}); err == nil {
		t.Error("unknown itag: want error")
	}
}

func TestOutputPathAndExists(t *testing.T) {
	dir := t.TempDir()
	p := OutputPath(dir, "", "Big Buck Bunny!", "aqz-KE-bpKQ", "mp3")
	if filepath.Base(p) != "big-buck-bunny.mp3" {
		t.Fatalf("path = %s", p)
	}
	if Exists(dir, "Big Buck Bunny!", "aqz-KE-bpKQ", "mp3") {
		t.Error("Exists before writing")
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir, "Big Buck Bunny!", "aqz-KE-bpKQ", "mp3") {
		t.Error("Exists after writing")
	}
	if p2 := OutputPath(dir, "", "Big Buck Bunny!", "aqz-KE-bpKQ", "mp3"); filepath.Base(p2) != "big-buck-bunny-1.mp3" {
		t.Errorf("second path = %s", p2)
	}
	if p3 := OutputPath(dir, "custom.mp4", "x", "id", "mp4"); filepath.Base(p3) != "custom.mp4" {
		t.Errorf("custom name = %s", p3)
	}
	if p4 := OutputPath(dir, "", "!!!", "aqz-KE-bpKQ", "m4a"); filepath.Base(p4) != "aqz-KE-bpKQ.m4a" {
		t.Errorf("fallback to id = %s", p4)
	}
}

func TestOutputPathInvalidParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Stat returns ENOTDIR, not ErrNotExist on Unix. This must terminate.
	if got := OutputPath(parent, "", "title", "id", "mp4"); got != filepath.Join(parent, "title.mp4") {
		t.Fatal(got)
	}
}
