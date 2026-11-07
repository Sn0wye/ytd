package media

import (
	"errors"
	"fmt"
	"io"
	"math"
)

// Matroska element IDs (with their length marker bits, as written on disk).
const (
	idEBML            = 0x1A45DFA3
	idSegment         = 0x18538067
	idSeekHead        = 0x114D9B74
	idInfo            = 0x1549A966
	idTimecodeScale   = 0x2AD7B1
	idTracks          = 0x1654AE6B
	idTrackEntry      = 0xAE
	idTrackNumber     = 0xD7
	idTrackType       = 0x83
	idCodecID         = 0x86
	idCodecPrivate    = 0x63A2
	idCodecDelay      = 0x56AA
	idSeekPreRoll     = 0x56BB
	idDefaultDuration = 0x23E383
	idVideo           = 0xE0
	idPixelWidth      = 0xB0
	idPixelHeight     = 0xBA
	idColour          = 0x55B0

	idMatrixCoefficients      = 0x55B1
	idRange                   = 0x55B9
	idTransferCharacteristics = 0x55BA
	idPrimaries               = 0x55BB
	idMaxCLL                  = 0x55BC
	idMaxFALL                 = 0x55BD
	idMasteringMetadata       = 0x55D0
	idPrimaryRChromaticityX   = 0x55D1
	idPrimaryRChromaticityY   = 0x55D2
	idPrimaryGChromaticityX   = 0x55D3
	idPrimaryGChromaticityY   = 0x55D4
	idPrimaryBChromaticityX   = 0x55D5
	idPrimaryBChromaticityY   = 0x55D6
	idWhitePointChromaticityX = 0x55D7
	idWhitePointChromaticityY = 0x55D8
	idLuminanceMax            = 0x55D9
	idLuminanceMin            = 0x55DA
	idAudio                   = 0xE1
	idSamplingFreq            = 0xB5
	idChannels                = 0x9F
	idCluster                 = 0x1F43B675
	idTimecode                = 0xE7
	idSimpleBlock             = 0xA3
	idBlockGroup              = 0xA0
	idBlock                   = 0xA1
	idReferenceBlock          = 0xFB
	idDiscardPadding          = 0x75A2
	idCues                    = 0x1C53BB6B
	idTags                    = 0x1254C367
)

const unknownSize = -1

// ebmlReader reads elements from an io.ReaderAt through a small window
// cache, so walking thousands of block headers costs few syscalls.
type ebmlReader struct {
	r    io.ReaderAt
	size int64
	win  []byte
	wpos int64
}

func (er *ebmlReader) bytesAt(pos int64, n int) ([]byte, error) {
	if pos < er.wpos || pos+int64(n) > er.wpos+int64(len(er.win)) {
		want := max(n, 256<<10)
		if rem := er.size - pos; int64(want) > rem {
			want = int(rem)
		}
		if want < n {
			return nil, io.ErrUnexpectedEOF
		}
		buf := make([]byte, want)
		if _, err := er.r.ReadAt(buf, pos); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		er.win, er.wpos = buf, pos
	}
	o := pos - er.wpos
	return er.win[o : o+int64(n)], nil
}

// header reads an element header at pos and returns its ID, payload size
// (unknownSize if unknown) and header length.
func (er *ebmlReader) header(pos int64) (id uint32, size int64, hlen int, err error) {
	avail := min(int64(12), er.size-pos)
	b, err := er.bytesAt(pos, int(avail))
	if err != nil {
		return 0, 0, 0, err
	}
	idLen := vintLen(b)
	if idLen == 0 || idLen > 4 || idLen >= len(b) {
		return 0, 0, 0, fmt.Errorf("webm: bad element ID at %d", pos)
	}
	for _, c := range b[:idLen] {
		id = id<<8 | uint32(c)
	}
	sl := vintLen(b[idLen:])
	if sl == 0 || idLen+sl > len(b) {
		return 0, 0, 0, fmt.Errorf("webm: bad element size at %d", pos)
	}
	v := uint64(b[idLen] & (0xff >> sl))
	allOnes := v == uint64(0xff>>sl)
	for _, c := range b[idLen+1 : idLen+sl] {
		v = v<<8 | uint64(c)
		allOnes = allOnes && c == 0xff
	}
	if allOnes {
		return id, unknownSize, idLen + sl, nil
	}
	return id, int64(v), idLen + sl, nil
}

func vintLen(b []byte) int {
	if len(b) == 0 || b[0] == 0 {
		return 0
	}
	n := 1
	for m := byte(0x80); b[0]&m == 0; m >>= 1 {
		n++
	}
	return n
}

// children iterates the elements in [start, end).
func (er *ebmlReader) children(start, end int64, fn func(id uint32, pos int64, size int64, hlen int) error) error {
	for pos := start; pos < end; {
		id, size, hlen, err := er.header(pos)
		if err != nil {
			return err
		}
		if size == unknownSize {
			if id != idSegment {
				return fmt.Errorf("webm: unknown-size element %x not supported", id)
			}
			size = end - pos - int64(hlen)
		}
		if pos+int64(hlen)+size > end {
			return fmt.Errorf("webm: element %x overruns its parent", id)
		}
		if err := fn(id, pos+int64(hlen), size, hlen); err != nil {
			return err
		}
		pos += int64(hlen) + size
	}
	return nil
}

func (er *ebmlReader) uint(pos, size int64) (uint64, error) {
	b, err := er.bytesAt(pos, int(size))
	if err != nil || size > 8 {
		return 0, fmt.Errorf("webm: bad uint at %d", pos)
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

func (er *ebmlReader) float(pos, size int64) (float64, error) {
	v, err := er.uint(pos, size)
	switch {
	case err != nil:
		return 0, err
	case size == 4:
		return float64(math.Float32frombits(uint32(v))), nil
	case size == 8:
		return math.Float64frombits(v), nil
	}
	return 0, fmt.Errorf("webm: bad float size %d", size)
}

func (er *ebmlReader) bytes(pos, size int64) ([]byte, error) {
	b, err := er.bytesAt(pos, int(size))
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), b...), nil
}

// openWebM reads a single-track WebM file as produced by YouTube.
func openWebM(r io.ReaderAt, size int64) (*Track, error) {
	er := &ebmlReader{r: r, size: size}
	t := &Track{Src: r}
	timecodeScale := uint64(1_000_000)
	trackNum := uint64(0)
	var defaultDur uint64

	parseTrack := func(start, end int64) error {
		return er.children(start, end, func(id uint32, pos, sz int64, _ int) error {
			var err error
			switch id {
			case idTrackNumber:
				trackNum, err = er.uint(pos, sz)
			case idTrackType:
				var v uint64
				v, err = er.uint(pos, sz)
				if v == 1 {
					t.Kind = Video
				} else {
					t.Kind = Audio
				}
			case idCodecID:
				var b []byte
				b, err = er.bytes(pos, sz)
				switch string(b) {
				case "V_VP9":
					t.Codec = "vp9"
				case "V_AV1":
					t.Codec = "av1"
				case "A_OPUS":
					t.Codec = "opus"
				default:
					return fmt.Errorf("webm: unsupported codec %q", b)
				}
			case idCodecPrivate:
				t.CodecPrivate, err = er.bytes(pos, sz)
			case idCodecDelay:
				t.CodecDelay, err = er.uint(pos, sz)
			case idSeekPreRoll:
				t.SeekPreRoll, err = er.uint(pos, sz)
			case idDefaultDuration:
				defaultDur, err = er.uint(pos, sz)
			case idVideo:
				err = er.children(pos, pos+sz, func(id uint32, p, s int64, hl int) error {
					var e error
					var v uint64
					switch id {
					case idPixelWidth:
						v, e = er.uint(p, s)
						t.Width = int(v)
					case idPixelHeight:
						v, e = er.uint(p, s)
						t.Height = int(v)
					case idColour:
						t.colour, e = er.bytes(p-int64(hl), s+int64(hl))
					}
					return e
				})
			case idAudio:
				err = er.children(pos, pos+sz, func(id uint32, p, s int64, _ int) error {
					var e error
					switch id {
					case idSamplingFreq:
						t.SampleRate, e = er.float(p, s)
					case idChannels:
						var v uint64
						v, e = er.uint(p, s)
						t.Channels = int(v)
					}
					return e
				})
			}
			return err
		})
	}

	parseBlock := func(pos, sz int64, cluster int64, simple bool) (Sample, error) {
		b, err := er.bytesAt(pos, int(min(sz, 4)))
		if err != nil || len(b) < 4 || vintLen(b) != 1 {
			return Sample{}, fmt.Errorf("webm: bad block at %d", pos)
		}
		if uint64(b[0]&0x7f) != trackNum {
			return Sample{}, fmt.Errorf("webm: unexpected track %d", b[0]&0x7f)
		}
		flags := b[3]
		if flags&0x06 != 0 {
			return Sample{}, errors.New("webm: laced blocks are not supported")
		}
		rel := int64(int16(uint16(b[1])<<8 | uint16(b[2])))
		return Sample{
			Offset: pos + 4,
			Size:   uint32(sz - 4),
			DTS:    cluster + rel,
			Key:    !simple || flags&0x80 != 0,
		}, nil
	}

	segFound := false
	err := er.children(0, size, func(id uint32, pos, sz int64, _ int) error {
		if id != idSegment {
			return nil
		}
		segFound = true
		return er.children(pos, pos+sz, func(id uint32, pos, sz int64, _ int) error {
			switch id {
			case idInfo:
				return er.children(pos, pos+sz, func(id uint32, p, s int64, _ int) error {
					if id == idTimecodeScale {
						v, err := er.uint(p, s)
						if v > 0 {
							timecodeScale = v
						}
						return err
					}
					return nil
				})
			case idTracks:
				n := 0
				return er.children(pos, pos+sz, func(id uint32, p, s int64, _ int) error {
					if id != idTrackEntry {
						return nil
					}
					if n++; n > 1 {
						return errors.New("webm: multiple tracks are not supported")
					}
					return parseTrack(p, p+s)
				})
			case idCluster:
				var tc int64
				return er.children(pos, pos+sz, func(id uint32, p, s int64, _ int) error {
					switch id {
					case idTimecode:
						v, err := er.uint(p, s)
						tc = int64(v)
						return err
					case idSimpleBlock:
						smp, err := parseBlock(p, s, tc, true)
						if err == nil {
							t.Samples = append(t.Samples, smp)
						}
						return err
					case idBlockGroup:
						var smp Sample
						var have bool
						ref := false
						var discard int64
						err := er.children(p, p+s, func(id uint32, bp, bs int64, _ int) error {
							switch id {
							case idBlock:
								var err error
								smp, err = parseBlock(bp, bs, tc, false)
								have = err == nil
								return err
							case idReferenceBlock:
								ref = true
							case idDiscardPadding:
								v, err := er.uint(bp, bs)
								// signed integer
								if bs > 0 && bs < 8 && v&(1<<(8*bs-1)) != 0 {
									v |= ^uint64(0) << (8 * bs)
								}
								discard = int64(v)
								return err
							}
							return nil
						})
						if err != nil {
							return err
						}
						if have {
							smp.Key = !ref
							smp.Discard = discard
							t.Samples = append(t.Samples, smp)
						}
					}
					return nil
				})
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if !segFound || t.Codec == "" {
		return nil, errors.New("webm: no track found")
	}
	if len(t.Samples) == 0 {
		return nil, errors.New("webm: no frames")
	}

	// Timestamps are in TimecodeScale units (1 ms for YouTube).
	t.Timescale = uint32(1_000_000_000 / timecodeScale)
	t.defaultDuration = defaultDur
	last := uint32(0)
	if defaultDur > 0 {
		last = uint32(defaultDur / timecodeScale)
	}
	for i := range t.Samples {
		if i+1 < len(t.Samples) {
			d := t.Samples[i+1].DTS - t.Samples[i].DTS
			if d > 0 {
				t.Samples[i].Dur = uint32(d)
				last = uint32(d)
			}
		} else {
			t.Samples[i].Dur = last
		}
	}
	return t, nil
}
