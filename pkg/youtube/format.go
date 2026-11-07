package youtube

import (
	"fmt"
	"mime"
	"sort"
	"strconv"
	"strings"
)

// Format is one adaptive (video-only or audio-only) stream.
type Format struct {
	Itag       int
	URL        string
	MimeType   string
	Ext        string // file extension of the stream: mp4, m4a or webm
	Codec      string // normalized: h264, av1, vp9, aac, opus, ...
	CodecRaw   string // as reported, e.g. avc1.64002A
	Video      bool
	Width      int
	Height     int
	FPS        int
	HDR        string // "", "HDR10" or "HLG"
	Quality    string // YouTube's label, e.g. "2160p60 HDR"
	Bitrate    int    // average bits per second
	Size       int64  // bytes; 0 if unknown
	Channels   int
	SampleRate int
	Language   string // audio track display name, for multi-language videos
	DefaultLng bool   // the original/default audio track
	DRC        bool   // dynamic range compressed audio variant
}

type rawFormat struct {
	Itag            int    `json:"itag"`
	URL             string `json:"url"`
	SignatureCipher string `json:"signatureCipher"`
	MimeType        string `json:"mimeType"`
	Bitrate         int    `json:"bitrate"`
	AverageBitrate  int    `json:"averageBitrate"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	FPS             int    `json:"fps"`
	QualityLabel    string `json:"qualityLabel"`
	ContentLength   string `json:"contentLength"`
	AudioChannels   int    `json:"audioChannels"`
	AudioSampleRate string `json:"audioSampleRate"`
	IsDrc           bool   `json:"isDrc"`
	AudioTrack      *struct {
		DisplayName    string `json:"displayName"`
		AudioIsDefault bool   `json:"audioIsDefault"`
	} `json:"audioTrack"`
	ColorInfo *struct {
		TransferCharacteristics string `json:"transferCharacteristics"`
	} `json:"colorInfo"`
}

func (r rawFormat) parse() (Format, bool) {
	// Ciphered URLs need the player JS; the visionOS client does not return them.
	if r.URL == "" {
		return Format{}, false
	}
	mt, params, err := mime.ParseMediaType(r.MimeType)
	if err != nil {
		return Format{}, false
	}
	kind, sub, _ := strings.Cut(mt, "/")
	codec := strings.TrimSpace(strings.Split(params["codecs"], ",")[0])

	f := Format{
		Itag:     r.Itag,
		URL:      r.URL,
		MimeType: r.MimeType,
		CodecRaw: codec,
		Codec:    NormalizeCodec(codec),
		Video:    kind == "video",
		Width:    r.Width,
		Height:   r.Height,
		FPS:      r.FPS,
		Quality:  r.QualityLabel,
		Bitrate:  r.AverageBitrate,
		Channels: r.AudioChannels,
		DRC:      r.IsDrc,
	}
	if f.Bitrate == 0 {
		f.Bitrate = r.Bitrate
	}
	f.Size, _ = strconv.ParseInt(r.ContentLength, 10, 64)
	f.SampleRate, _ = strconv.Atoi(r.AudioSampleRate)
	if r.AudioTrack != nil {
		f.Language = r.AudioTrack.DisplayName
		f.DefaultLng = r.AudioTrack.AudioIsDefault
	}
	if r.ColorInfo != nil {
		switch r.ColorInfo.TransferCharacteristics {
		case "COLOR_TRANSFER_CHARACTERISTICS_SMPTEST2084":
			f.HDR = "HDR10"
		case "COLOR_TRANSFER_CHARACTERISTICS_ARIB_STD_B67":
			f.HDR = "HLG"
		}
	}

	switch {
	case sub == "webm":
		f.Ext = "webm"
	case sub == "mp4" && kind == "audio":
		f.Ext = "m4a"
	case sub == "mp4":
		f.Ext = "mp4"
	default:
		return Format{}, false
	}
	// Streams without a length are live/post-live segments, not plain files.
	if f.Size <= 0 || (f.Video && f.Height == 0) {
		return Format{}, false
	}
	return f, true
}

// NormalizeCodec maps codec strings like "avc1.64002A" or "mp4a.40.2" to short names.
func NormalizeCodec(c string) string {
	c = strings.ToLower(c)
	switch {
	case strings.HasPrefix(c, "avc"):
		return "h264"
	case strings.HasPrefix(c, "hev"), strings.HasPrefix(c, "hvc"):
		return "hevc"
	case strings.HasPrefix(c, "av01"):
		return "av1"
	case strings.HasPrefix(c, "vp09"), strings.HasPrefix(c, "vp9"):
		return "vp9"
	case strings.HasPrefix(c, "mp4a"):
		return "aac"
	case c == "opus":
		return "opus"
	case c == "ac-3":
		return "ac3"
	case c == "ec-3":
		return "eac3"
	}
	return c
}

var codecLabels = map[string]string{
	"h264": "H.264", "hevc": "HEVC", "av1": "AV1", "vp9": "VP9",
	"aac": "AAC", "opus": "Opus", "ac3": "AC-3", "eac3": "E-AC-3",
}

// CodecLabel returns a human readable codec name.
func (f Format) CodecLabel() string {
	if l, ok := codecLabels[f.Codec]; ok {
		return l
	}
	return f.Codec
}

// Resolution returns e.g. "1080p60" or "2160p60 HDR10". YouTube's own label
// is used when present: it names quality classes, so a 3840x1600 ultrawide
// stream is "2160p", not "1600p".
func (f Format) Resolution() string {
	if f.Quality != "" {
		q := strings.TrimSuffix(f.Quality, " HDR")
		if f.HDR != "" {
			q += " " + f.HDR
		}
		return q
	}
	// Use the short side so vertical videos (shorts) read as 1080p, not 1920p.
	h := f.Height
	if f.Width > 0 && f.Width < h {
		h = f.Width
	}
	s := fmt.Sprintf("%dp", h)
	if f.FPS > 30 {
		s += strconv.Itoa(f.FPS)
	}
	if f.HDR != "" {
		s += " " + f.HDR
	}
	return s
}

var videoCodecRank = map[string]int{"h264": 0, "av1": 1, "vp9": 2}

func rank(c string) int {
	if r, ok := videoCodecRank[c]; ok {
		return r
	}
	return len(videoCodecRank)
}

// VideoFormats returns the video streams, best first: resolution, frame rate,
// codec compatibility (H.264 > AV1 > VP9), bitrate.
func (v *Video) VideoFormats() []Format {
	var list []Format
	for _, f := range v.Formats {
		if f.Video {
			list = append(list, f)
		}
	}
	sort.SliceStable(list, func(a, b int) bool {
		x, y := list[a], list[b]
		if x.Height*x.Width != y.Height*y.Width {
			return x.Height*x.Width > y.Height*y.Width
		}
		if x.FPS != y.FPS {
			return x.FPS > y.FPS
		}
		if x.HDR != y.HDR {
			return x.HDR == ""
		}
		if rx, ry := rank(x.Codec), rank(y.Codec); rx != ry {
			return rx < ry
		}
		return x.Bitrate > y.Bitrate
	})
	return list
}

// AudioFormats returns the audio streams without the dynamic-range-compressed
// duplicates, original language first, then by bitrate.
func (v *Video) AudioFormats() []Format {
	var list []Format
	for _, f := range v.Formats {
		if !f.Video && !f.DRC {
			list = append(list, f)
		}
	}
	sort.SliceStable(list, func(a, b int) bool {
		x, y := list[a], list[b]
		if x.DefaultLng != y.DefaultLng {
			return x.DefaultLng
		}
		if x.Language != y.Language {
			return x.Language < y.Language
		}
		if x.Bitrate != y.Bitrate {
			return x.Bitrate > y.Bitrate
		}
		return x.Codec == "aac" && y.Codec != "aac"
	})
	return list
}
