package youtube

import (
	"encoding/json"
	"testing"
)

const sampleFormats = `[
 {"itag":137,"url":"u","mimeType":"video/mp4; codecs=\"avc1.640028\"","bitrate":4000000,"averageBitrate":3000000,"width":1920,"height":1080,"fps":30,"contentLength":"100"},
 {"itag":248,"url":"u","mimeType":"video/webm; codecs=\"vp9\"","bitrate":3000000,"width":1920,"height":1080,"fps":30,"contentLength":"90"},
 {"itag":299,"url":"u","mimeType":"video/mp4; codecs=\"avc1.64002A\"","averageBitrate":6000000,"width":1920,"height":1080,"fps":60,"contentLength":"200"},
 {"itag":337,"url":"u","mimeType":"video/webm; codecs=\"vp09.02.51.10.01.09.16.09.00\"","averageBitrate":9000000,"width":3840,"height":2160,"fps":60,"contentLength":"300","colorInfo":{"transferCharacteristics":"COLOR_TRANSFER_CHARACTERISTICS_SMPTEST2084"}},
 {"itag":313,"url":"u","mimeType":"video/webm; codecs=\"vp9\"","averageBitrate":8000000,"width":3840,"height":2160,"fps":30,"contentLength":"280"},
 {"itag":616,"url":"u","mimeType":"video/mp4; codecs=\"vp09.00.40.08\"","averageBitrate":5000000,"width":1920,"height":1080,"fps":30},
 {"itag":18,"signatureCipher":"s=x","mimeType":"video/mp4; codecs=\"avc1.42001E, mp4a.40.2\"","width":640,"height":360,"contentLength":"10"},
 {"itag":140,"url":"u","mimeType":"audio/mp4; codecs=\"mp4a.40.2\"","averageBitrate":129000,"audioChannels":2,"audioSampleRate":"44100","contentLength":"50"},
 {"itag":140,"url":"u","mimeType":"audio/mp4; codecs=\"mp4a.40.2\"","averageBitrate":129000,"audioChannels":2,"isDrc":true,"contentLength":"50"},
 {"itag":251,"url":"u","mimeType":"audio/webm; codecs=\"opus\"","averageBitrate":135000,"audioChannels":2,"audioSampleRate":"48000","contentLength":"52"},
 {"itag":251,"url":"u","mimeType":"audio/webm; codecs=\"opus\"","averageBitrate":140000,"audioChannels":2,"contentLength":"53","audioTrack":{"displayName":"Spanish","audioIsDefault":false}},
 {"itag":139,"url":"u","mimeType":"audio/mp4; codecs=\"mp4a.40.5\"","averageBitrate":48000,"audioChannels":2,"contentLength":"20"}
]`

func sampleVideo(t *testing.T) *Video {
	var raw []rawFormat
	if err := json.Unmarshal([]byte(sampleFormats), &raw); err != nil {
		t.Fatal(err)
	}
	v := &Video{}
	for _, r := range raw {
		if f, ok := r.parse(); ok {
			v.Formats = append(v.Formats, f)
		}
	}
	return v
}

func TestParseFormats(t *testing.T) {
	v := sampleVideo(t)
	// 616 has no length (HLS-only), 18 is ciphered and muxed.
	if len(v.Formats) != 10 {
		t.Fatalf("got %d formats, want 10", len(v.Formats))
	}
	byItag := map[int]Format{}
	for _, f := range v.Formats {
		if !f.DRC {
			byItag[f.Itag] = f
		}
	}
	if f := byItag[140]; f.Ext != "m4a" || f.Codec != "aac" || f.SampleRate != 44100 || f.Video {
		t.Errorf("140 parsed as %+v", f)
	}
	if f := byItag[337]; f.Codec != "vp9" || f.HDR != "HDR10" || f.Resolution() != "2160p60 HDR10" || f.Ext != "webm" {
		t.Errorf("337 parsed as %+v (%s)", f, f.Resolution())
	}
	if f := byItag[137]; f.Bitrate != 3000000 || f.Codec != "h264" {
		t.Errorf("137 should use averageBitrate: %+v", f)
	}
	if f := byItag[248]; f.Bitrate != 3000000 {
		t.Errorf("248 should fall back to bitrate: %+v", f)
	}
}

func TestSorting(t *testing.T) {
	v := sampleVideo(t)
	var vids []int
	for _, f := range v.VideoFormats() {
		vids = append(vids, f.Itag)
	}
	want := []int{337, 313, 299, 137, 248} // size, then fps, then SDR, then codec
	if len(vids) != len(want) {
		t.Fatalf("video order %v, want %v", vids, want)
	}
	for i := range want {
		if vids[i] != want[i] {
			t.Fatalf("video order %v, want %v", vids, want)
		}
	}

	var auds []string
	for _, f := range v.AudioFormats() {
		auds = append(auds, f.Codec+":"+f.Language)
	}
	// DRC dropped, default language first by bitrate, other languages last.
	wantA := []string{"opus:", "aac:", "aac:", "opus:Spanish"}
	if len(auds) != len(wantA) {
		t.Fatalf("audio order %v, want %v", auds, wantA)
	}
	for i := range wantA {
		if auds[i] != wantA[i] {
			t.Fatalf("audio order %v, want %v", auds, wantA)
		}
	}
}

func TestNormalizeCodec(t *testing.T) {
	for in, want := range map[string]string{
		"avc1.64002A": "h264", "av01.0.13M.10": "av1", "vp9": "vp9", "vp9.2": "vp9",
		"vp09.02.51.10.01.09.16.09.00": "vp9", "mp4a.40.2": "aac", "opus": "opus", "ec-3": "eac3",
	} {
		if got := NormalizeCodec(in); got != want {
			t.Errorf("NormalizeCodec(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolutionLabel(t *testing.T) {
	f := Format{Width: 3840, Height: 1600, FPS: 60, Quality: "2160p60"}
	if got := f.Resolution(); got != "2160p60" {
		t.Errorf("ultrawide resolution = %q, want 2160p60", got)
	}
	f = Format{Width: 3840, Height: 2160, FPS: 60, Quality: "2160p60 HDR", HDR: "HDR10"}
	if got := f.Resolution(); got != "2160p60 HDR10" {
		t.Errorf("HDR resolution = %q", got)
	}
}

func TestResolutionVertical(t *testing.T) {
	f := Format{Width: 1080, Height: 1920, FPS: 30}
	if got := f.Resolution(); got != "1080p" {
		t.Errorf("vertical video resolution = %q", got)
	}
}
