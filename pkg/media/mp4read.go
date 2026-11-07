package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
)

var be = binary.BigEndian

// box is an ISO BMFF box parsed from memory.
type box struct {
	typ  string
	data []byte // payload without the header
	raw  []byte // whole box including the header
}

// boxes splits b into consecutive boxes.
func boxes(b []byte) ([]box, error) {
	var out []box
	for len(b) > 0 {
		if len(b) < 8 {
			return nil, errors.New("mp4: truncated box header")
		}
		size, hdr := uint64(be.Uint32(b)), 8
		typ := string(b[4:8])
		switch size {
		case 0:
			size = uint64(len(b))
		case 1:
			if len(b) < 16 {
				return nil, errors.New("mp4: truncated box header")
			}
			size, hdr = be.Uint64(b[8:]), 16
		}
		if size < uint64(hdr) || size > uint64(len(b)) {
			return nil, fmt.Errorf("mp4: bad size for box %q", typ)
		}
		out = append(out, box{typ: typ, data: b[hdr:size], raw: b[:size]})
		b = b[size:]
	}
	return out, nil
}

func child(b []byte, path ...string) (box, bool) {
	var cur box
	for _, p := range path {
		list, err := boxes(b)
		if err != nil {
			return box{}, false
		}
		found := false
		for _, x := range list {
			if x.typ == p {
				cur, found = x, true
				break
			}
		}
		if !found {
			return box{}, false
		}
		b = cur.data
	}
	return cur, true
}

type trex struct {
	dur, size, flags uint32
}

// openMP4 reads a fragmented MP4 as produced by YouTube (ftyp, moov with an
// empty sample table, sidx, then moof/mdat pairs).
func openMP4(r io.ReaderAt, size int64) (*Track, error) {
	t := &Track{Src: r}
	var defaults trex
	var trackID uint32
	haveMoov := false
	nextDTS := int64(0)

	for pos := int64(0); pos < size; {
		var hdr [16]byte
		n, err := r.ReadAt(hdr[:], pos)
		if n < 8 {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("mp4: reading box at %d: %w", pos, err)
		}
		bsize, typ := int64(be.Uint32(hdr[:])), string(hdr[4:8])
		switch bsize {
		case 0:
			bsize = size - pos
		case 1:
			if n < 16 {
				return nil, io.ErrUnexpectedEOF
			}
			bsize = int64(be.Uint64(hdr[8:]))
		}
		if bsize < 8 || pos+bsize > size {
			return nil, fmt.Errorf("mp4: bad box %q at %d", typ, pos)
		}

		switch typ {
		case "moov", "moof":
			buf := make([]byte, bsize)
			if _, err := r.ReadAt(buf, pos); err != nil {
				return nil, err
			}
			list, err := boxes(buf)
			if err != nil {
				return nil, err
			}
			if typ == "moov" {
				if trackID, defaults, err = parseMoov(t, list[0].data); err != nil {
					return nil, err
				}
				haveMoov = true
			} else {
				if !haveMoov {
					return nil, errors.New("mp4: moof before moov")
				}
				if nextDTS, err = parseMoof(t, list[0].data, pos, trackID, defaults, nextDTS); err != nil {
					return nil, err
				}
			}
		}
		pos += bsize
	}

	if !haveMoov {
		return nil, errors.New("mp4: no moov box")
	}
	if len(t.Samples) == 0 {
		return nil, errors.New("mp4: no samples (only fragmented MP4 is supported)")
	}
	if t.Codec == "av1" {
		if err := stripTemporalDelimiters(t); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// stripTemporalDelimiters drops the leading temporal delimiter OBU that
// YouTube keeps in AV1 samples; the MP4 and Matroska AV1 mappings say it
// should be removed (ffmpeg does the same).
func stripTemporalDelimiters(t *Track) error {
	var b [2]byte
	for i := range t.Samples {
		s := &t.Samples[i]
		if s.Size < 2 {
			continue
		}
		if _, err := t.Src.ReadAt(b[:], s.Offset); err != nil {
			return err
		}
		if b[0] == 0x12 && b[1] == 0x00 { // OBU_TEMPORAL_DELIMITER, has_size, size 0
			s.Offset += 2
			s.Size -= 2
		}
	}
	return nil
}

func parseMoov(t *Track, moov []byte) (uint32, trex, error) {
	var d trex
	if x, ok := child(moov, "mvex", "trex"); ok && len(x.data) >= 24 {
		d = trex{dur: be.Uint32(x.data[12:]), size: be.Uint32(x.data[16:]), flags: be.Uint32(x.data[20:])}
	}

	trak, ok := child(moov, "trak")
	if !ok {
		return 0, d, errors.New("mp4: no trak")
	}
	tkhd, ok := child(trak.data, "tkhd")
	if !ok || len(tkhd.data) < 24 {
		return 0, d, errors.New("mp4: no tkhd")
	}
	var trackID uint32
	if tkhd.data[0] == 1 {
		trackID = be.Uint32(tkhd.data[20:])
	} else {
		trackID = be.Uint32(tkhd.data[12:])
	}

	mdhd, ok := child(trak.data, "mdia", "mdhd")
	if !ok || len(mdhd.data) < 24 {
		return 0, d, errors.New("mp4: no mdhd")
	}
	if mdhd.data[0] == 1 {
		t.Timescale = be.Uint32(mdhd.data[20:])
	} else {
		t.Timescale = be.Uint32(mdhd.data[12:])
	}
	if t.Timescale == 0 {
		return 0, d, errors.New("mp4: zero timescale")
	}

	hdlr, ok := child(trak.data, "mdia", "hdlr")
	if !ok || len(hdlr.data) < 12 {
		return 0, d, errors.New("mp4: no hdlr")
	}
	switch string(hdlr.data[8:12]) {
	case "vide":
		t.Kind = Video
	case "soun":
		t.Kind = Audio
	default:
		return 0, d, fmt.Errorf("mp4: unsupported track type %q", hdlr.data[8:12])
	}

	if elst, ok := child(trak.data, "edts", "elst"); ok && len(elst.data) >= 8 {
		// Use the first non-empty edit (media_time >= 0).
		v, n := elst.data[0], int(be.Uint32(elst.data[4:]))
		p := elst.data[8:]
		for i := 0; i < n; i++ {
			var mt int64
			if v == 1 && len(p) >= 20 {
				mt, p = int64(be.Uint64(p[8:])), p[20:]
			} else if v == 0 && len(p) >= 12 {
				mt, p = int64(int32(be.Uint32(p[4:]))), p[12:]
			} else {
				break
			}
			if mt >= 0 {
				t.MediaTime = mt
				break
			}
		}
	}

	stsd, ok := child(trak.data, "mdia", "minf", "stbl", "stsd")
	if !ok || len(stsd.data) < 8 {
		return 0, d, errors.New("mp4: no stsd")
	}
	t.stsd = stsd.raw
	entries, err := boxes(stsd.data[8:])
	if err != nil || len(entries) == 0 {
		return 0, d, errors.New("mp4: empty stsd")
	}
	if err := parseSampleEntry(t, entries[0]); err != nil {
		return 0, d, err
	}
	return trackID, d, nil
}

// parseSampleEntry reads the codec, dimensions/audio format and the codec
// configuration from an stsd entry.
func parseSampleEntry(t *Track, e box) error {
	b := e.data
	var children []byte
	switch t.Kind {
	case Video:
		// VisualSampleEntry: 6 reserved, 2 dref index, 16 predefined, w, h, ... 78 bytes total.
		if len(b) < 78 {
			return errors.New("mp4: short visual sample entry")
		}
		t.Width, t.Height = int(be.Uint16(b[24:])), int(be.Uint16(b[26:]))
		children = b[78:]
	case Audio:
		// AudioSampleEntry: 8 reserved/dref, 8 reserved, channels, samplesize, 4, rate 16.16.
		if len(b) < 28 {
			return errors.New("mp4: short audio sample entry")
		}
		t.Channels = int(be.Uint16(b[16:]))
		t.SampleRate = float64(be.Uint32(b[24:]) >> 16)
		children = b[28:]
	}

	if t.Kind == Video {
		t.colour = colourFromMP4(children)
	}

	switch e.typ {
	case "avc1", "avc3":
		t.Codec = "h264"
		if c, ok := child(children, "avcC"); ok {
			t.CodecPrivate = c.data
		}
	case "av01":
		t.Codec = "av1"
		if c, ok := child(children, "av1C"); ok {
			t.CodecPrivate = c.data
		}
	case "mp4a":
		t.Codec = "aac"
		if c, ok := child(children, "esds"); ok {
			t.CodecPrivate = audioSpecificConfig(c.data)
		}
	default:
		return fmt.Errorf("mp4: unsupported codec %q", e.typ)
	}
	if t.CodecPrivate == nil {
		return fmt.Errorf("mp4: missing %s codec configuration", t.Codec)
	}
	return nil
}

// audioSpecificConfig extracts the DecoderSpecificInfo from an esds box payload.
func audioSpecificConfig(esds []byte) []byte {
	if len(esds) < 4 {
		return nil
	}
	b := esds[4:] // version + flags
	readDesc := func() (tag byte, body []byte, ok bool) {
		if len(b) < 2 {
			return 0, nil, false
		}
		tag, b = b[0], b[1:]
		n := 0
		for i := 0; i < 4 && len(b) > 0; i++ {
			c := b[0]
			b = b[1:]
			n = n<<7 | int(c&0x7f)
			if c&0x80 == 0 {
				break
			}
		}
		if n > len(b) {
			return 0, nil, false
		}
		return tag, b[:n], true
	}

	tag, es, ok := readDesc()
	if !ok || tag != 0x03 || len(es) < 3 {
		return nil
	}
	flags := es[2]
	es = es[3:]
	if flags&0x80 != 0 { // streamDependenceFlag
		es = es[min(2, len(es)):]
	}
	if flags&0x40 != 0 && len(es) > 0 { // URL_Flag
		es = es[min(1+int(es[0]), len(es)):]
	}
	if flags&0x20 != 0 { // OCRstreamFlag
		es = es[min(2, len(es)):]
	}
	b = es
	tag, dc, ok := readDesc()
	if !ok || tag != 0x04 || len(dc) < 13 {
		return nil
	}
	b = dc[13:]
	tag, dsi, ok := readDesc()
	if !ok || tag != 0x05 {
		return nil
	}
	return dsi
}

const sampleNonSync = 0x10000

func parseMoof(t *Track, moof []byte, moofPos int64, trackID uint32, d trex, nextDTS int64) (int64, error) {
	list, err := boxes(moof)
	if err != nil {
		return 0, err
	}
	for _, traf := range list {
		if traf.typ != "traf" {
			continue
		}
		parts, err := boxes(traf.data)
		if err != nil {
			return 0, err
		}

		base := moofPos
		dur, size, flags := d.dur, d.size, d.flags
		dataPos := int64(-1)
		skip := false
		for _, p := range parts {
			b := p.data
			switch p.typ {
			case "tfhd":
				if len(b) < 8 {
					return 0, errors.New("mp4: short tfhd")
				}
				fl := be.Uint32(b) & 0xffffff
				if be.Uint32(b[4:]) != trackID {
					skip = true
				}
				b = b[8:]
				read := func() uint32 {
					if len(b) < 4 {
						return 0
					}
					v := be.Uint32(b)
					b = b[4:]
					return v
				}
				if fl&0x1 != 0 {
					base = int64(read())<<32 | int64(read())
				}
				if fl&0x2 != 0 {
					read()
				}
				if fl&0x8 != 0 {
					dur = read()
				}
				if fl&0x10 != 0 {
					size = read()
				}
				if fl&0x20 != 0 {
					flags = read()
				}
			case "tfdt":
				if len(b) < 8 {
					return 0, errors.New("mp4: short tfdt")
				}
				if b[0] == 1 && len(b) >= 12 {
					nextDTS = int64(be.Uint64(b[4:]))
				} else {
					nextDTS = int64(be.Uint32(b[4:]))
				}
			case "trun":
				if skip {
					continue
				}
				if dataPos, nextDTS, err = parseTrun(t, b, base, dataPos, nextDTS, dur, size, flags); err != nil {
					return 0, err
				}
			}
		}
	}
	return nextDTS, nil
}

func parseTrun(t *Track, b []byte, base, dataPos, dts int64, dDur, dSize, dFlags uint32) (int64, int64, error) {
	if len(b) < 8 {
		return 0, 0, errors.New("mp4: short trun")
	}
	version, fl := b[0], be.Uint32(b)&0xffffff
	count := be.Uint32(b[4:])
	b = b[8:]
	read := func() (uint32, error) {
		if len(b) < 4 {
			return 0, errors.New("mp4: truncated trun")
		}
		v := be.Uint32(b)
		b = b[4:]
		return v, nil
	}

	if fl&0x1 != 0 {
		off, err := read()
		if err != nil {
			return 0, 0, err
		}
		dataPos = base + int64(int32(off))
	} else if dataPos < 0 {
		dataPos = base
	}
	firstFlags, hasFirst := uint32(0), fl&0x4 != 0
	if hasFirst {
		v, err := read()
		if err != nil {
			return 0, 0, err
		}
		firstFlags = v
	}

	t.Samples = slices.Grow(t.Samples, int(count))
	for i := uint32(0); i < count; i++ {
		s := Sample{Offset: dataPos, DTS: dts, Dur: dDur, Size: dSize}
		sf := dFlags
		var err error
		if fl&0x100 != 0 {
			if s.Dur, err = read(); err != nil {
				return 0, 0, err
			}
		}
		if fl&0x200 != 0 {
			if s.Size, err = read(); err != nil {
				return 0, 0, err
			}
		}
		if fl&0x400 != 0 {
			if sf, err = read(); err != nil {
				return 0, 0, err
			}
		} else if i == 0 && hasFirst {
			sf = firstFlags
		}
		if fl&0x800 != 0 {
			v, err := read()
			if err != nil {
				return 0, 0, err
			}
			if version == 0 {
				s.CTO = int32(min(v, 1<<31-1))
			} else {
				s.CTO = int32(v)
			}
		}
		s.Key = t.Kind == Audio || sf&sampleNonSync == 0
		t.Samples = append(t.Samples, s)
		dataPos += int64(s.Size)
		dts += int64(s.Dur)
	}
	return dataPos, dts, nil
}

// colourFromMP4 converts the colr (nclx), mdcv and clli boxes of a visual
// sample entry into a Matroska Colour element, so HDR metadata survives
// MP4 to WebM/MKV. Returns nil when the entry has none of them.
func colourFromMP4(children []byte) []byte {
	list, err := boxes(children)
	if err != nil {
		return nil
	}
	var body ebmlWriter
	for _, b := range list {
		d := b.data
		switch {
		case b.typ == "colr" && len(d) >= 11 && string(d[:4]) == "nclx":
			body.uint(idMatrixCoefficients, uint64(be.Uint16(d[8:])))
			if d[10]&0x80 != 0 {
				body.uint(idRange, 2) // full
			} else {
				body.uint(idRange, 1) // broadcast
			}
			body.uint(idTransferCharacteristics, uint64(be.Uint16(d[6:])))
			body.uint(idPrimaries, uint64(be.Uint16(d[4:])))
		case b.typ == "clli" && len(d) >= 4:
			body.uint(idMaxCLL, uint64(be.Uint16(d)))
			body.uint(idMaxFALL, uint64(be.Uint16(d[2:])))
		case b.typ == "mdcv" && len(d) >= 24:
			// SMPTE ST 2086 order: green, blue, red, white point; chromaticity
			// in 0.00002 units, luminance in 0.0001 cd/m².
			c := func(i int) float64 { return float64(be.Uint16(d[i:])) / 50000 }
			body.master(idMasteringMetadata, func(w *ebmlWriter) {
				w.float(idPrimaryRChromaticityX, c(8))
				w.float(idPrimaryRChromaticityY, c(10))
				w.float(idPrimaryGChromaticityX, c(0))
				w.float(idPrimaryGChromaticityY, c(2))
				w.float(idPrimaryBChromaticityX, c(4))
				w.float(idPrimaryBChromaticityY, c(6))
				w.float(idWhitePointChromaticityX, c(12))
				w.float(idWhitePointChromaticityY, c(14))
				w.float(idLuminanceMax, float64(be.Uint32(d[16:]))/10000)
				w.float(idLuminanceMin, float64(be.Uint32(d[20:]))/10000)
			})
		}
	}
	if body.Len() == 0 {
		return nil
	}
	var w ebmlWriter
	w.bin(idColour, body.Bytes())
	return w.Bytes()
}
