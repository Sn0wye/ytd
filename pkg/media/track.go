// Package media remuxes YouTube's separate audio and video streams into a
// single file without re-encoding and without ffmpeg. It reads fragmented
// MP4 (DASH) and WebM inputs and writes progressive MP4/M4A, Matroska/WebM
// and Ogg Opus.
package media

import (
	"fmt"
	"io"
	"slices"
)

// Kind is the type of a track.
type Kind int

const (
	Video Kind = iota
	Audio
)

// Sample is one coded frame stored in the source file.
type Sample struct {
	Offset  int64  // position in the source
	Size    uint32 // bytes
	DTS     int64  // decode timestamp, in track timescale
	CTO     int32  // composition offset: PTS = DTS + CTO
	Dur     uint32 // in track timescale
	Key     bool   // sync sample / keyframe
	Discard int64  // Matroska DiscardPadding in ns (Opus end trimming)
}

// Track is a demuxed elementary stream.
type Track struct {
	Kind      Kind
	Codec     string // h264, av1, vp9, aac, opus
	Timescale uint32 // ticks per second
	Samples   []Sample
	Src       io.ReaderAt

	// MediaTime is the edit-list start (in timescale units): the PTS that
	// should be presented at time zero. Non-zero for H.264 with B-frames.
	MediaTime int64

	// stsd is the raw MP4 sample description box (MP4 sources only).
	stsd []byte
	// CodecPrivate is the codec configuration in Matroska form: avcC or
	// av1C payload, AAC AudioSpecificConfig or OpusHead.
	CodecPrivate []byte
	// colour is the raw Matroska Colour element (WebM sources only).
	colour []byte

	Width, Height int
	SampleRate    float64
	Channels      int
	CodecDelay    uint64 // ns
	SeekPreRoll   uint64 // ns
	// defaultDuration is the source's Matroska DefaultDuration in ns.
	defaultDuration uint64
}

// Duration returns the track duration in timescale units.
func (t *Track) Duration() int64 {
	if len(t.Samples) == 0 {
		return 0
	}
	last := t.Samples[len(t.Samples)-1]
	return last.DTS + int64(last.Dur) - t.Samples[0].DTS
}

// presentationEnd returns the end of the last presented frame, relative to
// the presentation start, in timescale units.
func (t *Track) presentationEnd() int64 {
	var end int64
	for _, s := range t.Samples {
		end = max(end, s.DTS+int64(s.CTO)+int64(s.Dur))
	}
	return max(end-t.MediaTime, 0)
}

// Seconds converts a timescale value to seconds.
func (t *Track) Seconds(v int64) float64 { return float64(v) / float64(t.Timescale) }

// ptsNS returns the presentation time of sample i in nanoseconds, relative
// to the presentation start.
func (t *Track) ptsNS(i int) int64 {
	s := t.Samples[i]
	return scale(s.DTS+int64(s.CTO)-t.MediaTime, 1e9, int64(t.Timescale))
}

func scale(v, num, den int64) int64 {
	// v*num/den without overflow for realistic media durations.
	return v/den*num + v%den*num/den
}

// Containers that Mux can write.
var Containers = []string{"mp4", "mkv", "webm", "m4a", "opus"}

// Supports reports whether container can hold a stream with the given
// codec coming from a source file with extension ext, without re-encoding.
func Supports(container, codec, ext string) bool {
	switch container {
	case "mp4":
		// MP4 output copies the sample descriptions of MP4 sources.
		return ext != "webm" && slices.Contains([]string{"h264", "av1", "aac"}, codec)
	case "m4a":
		return ext == "m4a" && codec == "aac"
	case "webm":
		return slices.Contains([]string{"vp9", "av1", "opus"}, codec)
	case "mkv":
		return slices.Contains([]string{"h264", "vp9", "av1", "aac", "opus"}, codec)
	case "opus":
		return codec == "opus"
	}
	return false
}

// Open demuxes a downloaded stream. ext is the source extension (mp4, m4a or webm).
func Open(r io.ReaderAt, size int64, ext string) (*Track, error) {
	switch ext {
	case "mp4", "m4a":
		return openMP4(r, size)
	case "webm":
		return openWebM(r, size)
	}
	return nil, fmt.Errorf("unsupported source format %q", ext)
}
