package media

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func openTrack(t *testing.T, name string) *Track {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	st, _ := f.Stat()
	ext := filepath.Ext(name)[1:]
	tr, err := Open(f, st.Size(), ext)
	if err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	return tr
}

func TestDemux(t *testing.T) {
	cases := []struct {
		file  string
		kind  Kind
		codec string
	}{
		{"video.mp4", Video, "h264"},
		{"av1.mp4", Video, "av1"},
		{"audio.m4a", Audio, "aac"},
		{"video.webm", Video, "vp9"},
		{"audio.webm", Audio, "opus"},
	}
	for _, c := range cases {
		tr := openTrack(t, c.file)
		if tr.Kind != c.kind || tr.Codec != c.codec {
			t.Errorf("%s: got kind %v codec %s", c.file, tr.Kind, tr.Codec)
		}
		if c.kind == Video && (tr.Width != 64 || tr.Height != 64) {
			t.Errorf("%s: got %dx%d, want 64x64", c.file, tr.Width, tr.Height)
		}
		if c.kind == Video && len(tr.Samples) != 60 {
			t.Errorf("%s: got %d frames, want 60", c.file, len(tr.Samples))
		}
		if !tr.Samples[0].Key {
			t.Errorf("%s: first sample is not a keyframe", c.file)
		}
		if secs := tr.Seconds(tr.Duration()); secs < 1.9 || secs > 2.1 {
			t.Errorf("%s: duration %.3fs, want ~2s", c.file, secs)
		}
		if len(tr.CodecPrivate) == 0 && tr.Codec != "vp9" {
			t.Errorf("%s: no codec private data", c.file)
		}
	}

	if h264 := openTrack(t, "video.mp4"); h264.Samples[0].CTO <= 0 {
		t.Error("video.mp4: expected composition offsets (B-frames)")
	}
	if a := openTrack(t, "audio.m4a"); a.SampleRate != 44100 || a.Channels != 1 {
		t.Errorf("audio.m4a: got %v Hz %d ch", a.SampleRate, a.Channels)
	}
}

// mux muxes sources into container and returns the output path.
func mux(t *testing.T, container string, files ...string) string {
	t.Helper()
	var srcs []Source
	for _, f := range files {
		srcs = append(srcs, Source{Path: filepath.Join("testdata", f), Ext: filepath.Ext(f)[1:]})
	}
	out := filepath.Join(t.TempDir(), "out."+container)
	if err := Mux(out, container, srcs, Meta{Title: "Test", Artist: "ytd"}); err != nil {
		t.Fatalf("Mux %s %v: %v", container, files, err)
	}
	return out
}

// decodeWithFFmpeg fully decodes the file when ffmpeg is installed.
func decodeWithFFmpeg(t *testing.T, path string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return
	}
	out, err := exec.Command("ffmpeg", "-v", "error", "-xerror", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil || len(bytes.TrimSpace(out)) > 0 {
		t.Errorf("ffmpeg decode of %s: %v %s", filepath.Base(path), err, out)
	}
}

func TestMP4(t *testing.T) {
	for _, files := range [][]string{{"video.mp4", "audio.m4a"}, {"av1.mp4", "audio.m4a"}} {
		out := mux(t, "mp4", files...)
		b, _ := os.ReadFile(out)
		top, err := boxes(b)
		if err != nil {
			t.Fatal(err)
		}
		if len(top) != 3 || top[0].typ != "ftyp" || top[1].typ != "moov" || top[2].typ != "mdat" {
			t.Fatalf("want ftyp, moov, mdat; got %v", top)
		}

		var want []int
		var total int
		for _, f := range files {
			tr := openTrack(t, f)
			want = append(want, len(tr.Samples))
			for _, s := range tr.Samples {
				total += int(s.Size)
			}
		}
		if len(top[2].data) != total {
			t.Errorf("mdat holds %d bytes, want %d", len(top[2].data), total)
		}

		traks, _ := boxes(top[1].data)
		i := 0
		for _, trak := range traks {
			if trak.typ != "trak" {
				continue
			}
			stsz, ok := child(trak.data, "mdia", "minf", "stbl", "stsz")
			if !ok {
				t.Fatal("no stsz")
			}
			if n := int(binary.BigEndian.Uint32(stsz.data[8:])); n != want[i] {
				t.Errorf("track %d: %d samples, want %d", i, n, want[i])
			}
			i++
		}
		if i != 2 {
			t.Errorf("got %d tracks, want 2", i)
		}
		decodeWithFFmpeg(t, out)
	}

	out := mux(t, "m4a", "audio.m4a")
	if b, _ := os.ReadFile(out); !bytes.Equal(b[8:12], []byte("M4A ")) {
		t.Errorf("m4a brand = %q", b[8:12])
	}
	decodeWithFFmpeg(t, out)
}

// TestEditList checks the YouTube case: B-frames with an edit list that
// shifts presentation to start at zero. ffmpeg-made fragments carry no edit
// list, so the shift is set by hand, as openMP4 does from elst.
func TestEditList(t *testing.T) {
	v, a := openTrack(t, "video.mp4"), openTrack(t, "audio.m4a")
	v.MediaTime = int64(v.Samples[0].CTO)
	out := filepath.Join(t.TempDir(), "edit.mp4")
	f, _ := os.Create(out)
	if err := WriteMP4(f, []*Track{v, a}, Meta{}, false); err != nil {
		t.Fatal(err)
	}
	f.Close()

	b, _ := os.ReadFile(out)
	top, _ := boxes(b)
	traks, _ := boxes(top[1].data)
	elst, ok := child(traks[1].data, "edts", "elst")
	if !ok {
		t.Fatal("no edit list written")
	}
	if mt := int64(binary.BigEndian.Uint64(elst.data[16:])); mt != v.MediaTime {
		t.Errorf("elst media_time = %d, want %d", mt, v.MediaTime)
	}
	if _, err := exec.LookPath("ffprobe"); err == nil {
		got, _ := exec.Command("ffprobe", "-v", "error", "-select_streams", "v", "-show_entries", "stream=start_time", "-of", "csv=p=0", out).Output()
		if s := string(bytes.TrimSpace(got)); s != "0.000000" {
			t.Errorf("video start_time = %q, want 0", s)
		}
	}
	decodeWithFFmpeg(t, out)
}

func TestMatroska(t *testing.T) {
	cases := []struct {
		container string
		files     []string
	}{
		{"webm", []string{"video.webm", "audio.webm"}},
		{"webm", []string{"av1.mp4", "audio.webm"}},
		{"mkv", []string{"video.mp4", "audio.webm"}},
		{"mkv", []string{"video.webm", "audio.m4a"}},
	}
	for _, c := range cases {
		out := mux(t, c.container, c.files...)
		f, _ := os.Open(out)
		st, _ := f.Stat()
		er := &ebmlReader{r: f, size: st.Size()}

		blocks := map[byte]int{}
		var sawCues, sawSegment bool
		err := er.children(0, st.Size(), func(id uint32, pos, size int64, _ int) error {
			if id != idSegment {
				return nil
			}
			sawSegment = pos+size == st.Size()
			return er.children(pos, pos+size, func(id uint32, pos, size int64, _ int) error {
				switch id {
				case idCues:
					sawCues = true
				case idCluster:
					return er.children(pos, pos+size, func(id uint32, p, s int64, _ int) error {
						switch id {
						case idSimpleBlock:
							b, _ := er.bytesAt(p, 1)
							blocks[b[0]&0x7f]++
						case idBlockGroup:
							blocks[0]++ // only used for the last Opus frame (DiscardPadding)
						}
						return nil
					})
				}
				return nil
			})
		})
		f.Close()
		if err != nil {
			t.Fatalf("%v: walking output: %v", c.files, err)
		}
		if !sawSegment {
			t.Errorf("%v: segment size does not match the file", c.files)
		}
		if !sawCues {
			t.Errorf("%v: no Cues", c.files)
		}
		for i, file := range c.files {
			tr := openTrack(t, file)
			got := blocks[byte(i+1)]
			if tr.Codec == "opus" {
				got += blocks[0] // last Opus frame carries DiscardPadding in a BlockGroup
			}
			if got != len(tr.Samples) {
				t.Errorf("%v: track %d has %d blocks, want %d", c.files, i+1, got, len(tr.Samples))
			}
		}
		decodeWithFFmpeg(t, out)
	}

	src := []Source{{Path: "testdata/video.mp4", Ext: "mp4"}}
	if err := Mux(filepath.Join(t.TempDir(), "x.webm"), "webm", src, Meta{}); err == nil {
		t.Error("webm with H.264 should fail")
	}
}

func TestOggOpus(t *testing.T) {
	out := mux(t, "opus", "audio.webm")
	b, _ := os.ReadFile(out)
	tr := openTrack(t, "audio.webm")

	packets, pages := 0, 0
	for len(b) > 0 {
		if len(b) < 27 || string(b[:4]) != "OggS" {
			t.Fatalf("bad page at page %d", pages)
		}
		nseg := int(b[26])
		lacing := b[27 : 27+nseg]
		body := 0
		for _, l := range lacing {
			body += int(l)
			if l < 255 {
				packets++
			}
		}
		page := b[:27+nseg+body]
		crc := binary.LittleEndian.Uint32(page[22:])
		check := append([]byte(nil), page...)
		copy(check[22:26], []byte{0, 0, 0, 0})
		var c uint32
		for _, x := range check {
			c = c<<8 ^ oggCRC[byte(c>>24)^x]
		}
		if c != crc {
			t.Errorf("page %d: bad CRC", pages)
		}
		if pages == 0 && !bytes.HasPrefix(page[28:], []byte("OpusHead")) {
			t.Error("first packet is not OpusHead")
		}
		b = b[len(page):]
		pages++
	}
	if want := len(tr.Samples) + 2; packets != want {
		t.Errorf("got %d packets, want %d", packets, want)
	}
	decodeWithFFmpeg(t, out)
}

func TestOpusSamples(t *testing.T) {
	cases := map[byte]int64{
		0xFC: 960,  // CELT 20 ms, 1 frame
		0xF8: 960,  // CELT 20 ms mono
		0x78: 960,  // Hybrid FB 20 ms
		0x00: 480,  // SILK NB 10 ms
		0xFD: 1920, // CELT 20 ms, 2 frames
	}
	for toc, want := range cases {
		if got := opusSamples([]byte{toc, 0}); got != want {
			t.Errorf("toc %#x: got %d, want %d", toc, got, want)
		}
	}
	if got := opusSamples([]byte{0xFF, 0x03}); got != 3*960 {
		t.Errorf("code 3 with 3 frames: got %d", got)
	}
}

func TestSupports(t *testing.T) {
	yes := [][3]string{{"mp4", "h264", "mp4"}, {"mp4", "av1", "mp4"}, {"mp4", "aac", "m4a"}, {"webm", "vp9", "webm"},
		{"webm", "av1", "mp4"}, {"webm", "opus", "webm"}, {"mkv", "h264", "mp4"}, {"mkv", "opus", "webm"},
		{"m4a", "aac", "m4a"}, {"opus", "opus", "webm"}}
	no := [][3]string{{"mp4", "vp9", "webm"}, {"mp4", "opus", "webm"}, {"webm", "h264", "mp4"}, {"webm", "aac", "m4a"},
		{"m4a", "opus", "webm"}, {"mp3", "aac", "m4a"}}
	for _, c := range yes {
		if !Supports(c[0], c[1], c[2]) {
			t.Errorf("Supports(%v) = false", c)
		}
	}
	for _, c := range no {
		if Supports(c[0], c[1], c[2]) {
			t.Errorf("Supports(%v) = true", c)
		}
	}
}

func TestEBMLSize(t *testing.T) {
	for _, v := range []uint64{0, 1, 126, 127, 128, 16382, 16383, 1 << 20, 1<<56 - 2} {
		var w ebmlWriter
		w.size(v)
		b := w.Bytes()
		n := vintLen(b)
		if n != len(b) {
			t.Fatalf("size(%d): length marker %d, wrote %d bytes", v, n, len(b))
		}
		got := uint64(b[0] & (0xff >> n))
		for _, c := range b[1:] {
			got = got<<8 | uint64(c)
		}
		if got != v || got == (1<<(7*n))-1 {
			t.Errorf("size(%d) round-trips to %d (%x)", v, got, b)
		}
	}
}

// TestHDRColour uses the colr/mdcv/clli boxes of YouTube itag 701 (AV1 HDR10).
func TestHDRColour(t *testing.T) {
	var w boxWriter
	for _, b := range []struct{ typ, hex string }{
		{"colr", "6e636c7800090010000900"},
		{"mdcv", "2fda894e1af40a28846c3ee43d1340420098968000000001"},
		{"clli", "044c00b4"},
	} {
		data := make([]byte, len(b.hex)/2)
		for i := range data {
			var v byte
			for _, c := range b.hex[2*i : 2*i+2] {
				v = v<<4 | byte(bytes.IndexRune([]byte("0123456789abcdef"), c))
			}
			data[i] = v
		}
		w.box(b.typ, func() { w.Write(data) })
	}
	col := colourFromMP4(w.Bytes())
	if col == nil {
		t.Fatal("no Colour element")
	}
	er := &ebmlReader{r: bytes.NewReader(col), size: int64(len(col))}
	got := map[uint32]float64{}
	err := er.children(0, int64(len(col)), func(id uint32, pos, size int64, _ int) error {
		return er.children(pos, pos+size, func(id uint32, p, s int64, _ int) error {
			if id == idMasteringMetadata {
				return er.children(p, p+s, func(id uint32, p, s int64, _ int) error {
					v, err := er.float(p, s)
					got[id] = v
					return err
				})
			}
			v, err := er.uint(p, s)
			got[id] = float64(v)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint32]float64{
		idPrimaries: 9, idTransferCharacteristics: 16, idMatrixCoefficients: 9, idRange: 1,
		idMaxCLL: 1100, idMaxFALL: 180,
		idPrimaryRChromaticityX: 0.678, idPrimaryGChromaticityY: 0.703, idPrimaryBChromaticityX: 0.138,
		idWhitePointChromaticityX: 0.3127, idLuminanceMax: 1000, idLuminanceMin: 0.0001,
	}
	for id, v := range want {
		if d := got[id] - v; d > 1e-9 || d < -1e-9 {
			t.Errorf("element %#x = %v, want %v", id, got[id], v)
		}
	}
}
