package media

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
)

// Meta is written into the output file's tags.
type Meta struct {
	Title  string
	Artist string
}

// run is a group of consecutive samples of one track (an MP4 chunk).
type run struct {
	track, first, count int
}

// interleave orders samples from all tracks by decode time in groups of at
// most span seconds, so players can read audio and video together.
func interleave(tracks []*Track, span float64) []run {
	next := make([]int, len(tracks))
	var out []run
	for {
		best, bestT := -1, math.Inf(1)
		for i, t := range tracks {
			if next[i] < len(t.Samples) {
				if ts := t.Seconds(t.Samples[next[i]].DTS); ts < bestT {
					best, bestT = i, ts
				}
			}
		}
		if best < 0 {
			return out
		}
		t := tracks[best]
		r := run{track: best, first: next[best]}
		for next[best] < len(t.Samples) && (r.count == 0 || t.Seconds(t.Samples[next[best]].DTS) < bestT+span) {
			next[best]++
			r.count++
		}
		out = append(out, r)
	}
}

// boxWriter builds ISO BMFF boxes in memory.
type boxWriter struct{ bytes.Buffer }

func (w *boxWriter) u8(v uint8)   { w.WriteByte(v) }
func (w *boxWriter) u16(v uint16) { w.Write(binary.BigEndian.AppendUint16(nil, v)) }
func (w *boxWriter) u32(v uint32) { w.Write(binary.BigEndian.AppendUint32(nil, v)) }
func (w *boxWriter) u64(v uint64) { w.Write(binary.BigEndian.AppendUint64(nil, v)) }
func (w *boxWriter) zeros(n int)  { w.Write(make([]byte, n)) }

func (w *boxWriter) box(typ string, body func()) {
	start := w.Len()
	w.u32(0)
	w.WriteString(typ)
	body()
	binary.BigEndian.PutUint32(w.Bytes()[start:], uint32(w.Len()-start))
}

func (w *boxWriter) fullBox(typ string, version uint8, flags uint32, body func()) {
	w.box(typ, func() {
		w.u32(uint32(version)<<24 | flags)
		body()
	})
}

var unityMatrix = []uint32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}

const movieTimescale = 1000

// WriteMP4 writes a progressive MP4 (moov before mdat, "faststart") holding
// the given tracks, copying the coded samples unchanged. m4a selects the
// audio-only brand.
func WriteMP4(out *os.File, tracks []*Track, meta Meta, m4a bool) error {
	for _, t := range tracks {
		if t.stsd == nil {
			return errors.New("mp4: track has no MP4 sample description (WebM sources cannot be written to MP4)")
		}
	}
	runs := interleave(tracks, 0.5)

	var mdatSize int64
	for _, t := range tracks {
		for _, s := range t.Samples {
			mdatSize += int64(s.Size)
		}
	}
	co64 := mdatSize > math.MaxUint32-(64<<20)
	mdatHdr := int64(8)
	if mdatSize+8 > math.MaxUint32 {
		mdatHdr = 16
	}

	ftyp := buildFtyp(tracks, m4a)
	// First pass with zero offsets to learn the moov size; offsets have a
	// fixed width so the second pass produces the same size.
	moov := buildMoov(tracks, runs, nil, co64, meta)
	dataStart := int64(len(ftyp)+len(moov)) + mdatHdr

	offsets := make([]int64, len(runs))
	pos := dataStart
	for i, r := range runs {
		offsets[i] = pos
		for _, s := range tracks[r.track].Samples[r.first : r.first+r.count] {
			pos += int64(s.Size)
		}
	}
	moov = buildMoov(tracks, runs, offsets, co64, meta)

	w := bufio.NewWriterSize(out, 4<<20)
	w.Write(ftyp)
	w.Write(moov)
	if mdatHdr == 16 {
		w.Write(binary.BigEndian.AppendUint32(nil, 1))
		w.WriteString("mdat")
		w.Write(binary.BigEndian.AppendUint64(nil, uint64(mdatSize+16)))
	} else {
		w.Write(binary.BigEndian.AppendUint32(nil, uint32(mdatSize+8)))
		w.WriteString("mdat")
	}
	for _, r := range runs {
		t := tracks[r.track]
		if err := copySamples(w, t.Src, t.Samples[r.first:r.first+r.count]); err != nil {
			return err
		}
	}
	return w.Flush()
}

// copySamples copies sample data, merging samples that are contiguous in the source.
func copySamples(w io.Writer, src io.ReaderAt, samples []Sample) error {
	for i := 0; i < len(samples); {
		start, end := samples[i].Offset, samples[i].Offset+int64(samples[i].Size)
		j := i + 1
		for j < len(samples) && samples[j].Offset == end {
			end += int64(samples[j].Size)
			j++
		}
		if _, err := io.Copy(w, io.NewSectionReader(src, start, end-start)); err != nil {
			return err
		}
		i = j
	}
	return nil
}

func buildFtyp(tracks []*Track, m4a bool) []byte {
	var w boxWriter
	w.box("ftyp", func() {
		if m4a {
			w.WriteString("M4A ")
			w.u32(0x200)
			w.WriteString("M4A isomiso2mp41")
			return
		}
		w.WriteString("isom")
		w.u32(0x200)
		w.WriteString("isomiso2mp41")
		for _, t := range tracks {
			switch t.Codec {
			case "h264":
				w.WriteString("avc1")
			case "av1":
				w.WriteString("av01")
			}
		}
	})
	return w.Bytes()
}

// presentation returns the track's duration in movie timescale units,
// excluding the edit-list start offset.
func presentation(t *Track) uint64 {
	return uint64(scale(t.presentationEnd(), movieTimescale, int64(t.Timescale)))
}

func buildMoov(tracks []*Track, runs []run, offsets []int64, co64 bool, meta Meta) []byte {
	var movieDur uint64
	for _, t := range tracks {
		movieDur = max(movieDur, presentation(t))
	}

	var w boxWriter
	w.box("moov", func() {
		w.fullBox("mvhd", 1, 0, func() {
			w.u64(0) // creation
			w.u64(0) // modification
			w.u32(movieTimescale)
			w.u64(movieDur)
			w.u32(0x10000) // rate
			w.u16(0x100)   // volume
			w.zeros(10)
			for _, m := range unityMatrix {
				w.u32(m)
			}
			w.zeros(24)
			w.u32(uint32(len(tracks) + 1))
		})
		for i, t := range tracks {
			writeTrak(&w, t, uint32(i+1), i, runs, offsets, co64)
		}
		writeUdta(&w, meta)
	})
	return w.Bytes()
}

func writeTrak(w *boxWriter, t *Track, id uint32, ti int, runs []run, offsets []int64, co64 bool) {
	w.box("trak", func() {
		w.fullBox("tkhd", 1, 0x3, func() { // enabled | in movie
			w.u64(0)
			w.u64(0)
			w.u32(id)
			w.u32(0)
			w.u64(presentation(t))
			w.zeros(8)
			w.u16(0) // layer
			w.u16(0) // alternate group
			if t.Kind == Audio {
				w.u16(0x100)
			} else {
				w.u16(0)
			}
			w.u16(0)
			for _, m := range unityMatrix {
				w.u32(m)
			}
			w.u32(uint32(t.Width) << 16)
			w.u32(uint32(t.Height) << 16)
		})
		if t.MediaTime > 0 {
			w.box("edts", func() {
				w.fullBox("elst", 1, 0, func() {
					w.u32(1)
					w.u64(presentation(t))
					w.u64(uint64(t.MediaTime))
					w.u32(0x10000)
				})
			})
		}
		w.box("mdia", func() {
			w.fullBox("mdhd", 1, 0, func() {
				w.u64(0)
				w.u64(0)
				w.u32(t.Timescale)
				w.u64(uint64(t.Duration()))
				w.u16(0x55C4) // "und"
				w.u16(0)
			})
			w.fullBox("hdlr", 0, 0, func() {
				w.u32(0)
				if t.Kind == Video {
					w.WriteString("vide")
				} else {
					w.WriteString("soun")
				}
				w.zeros(12)
				if t.Kind == Video {
					w.WriteString("VideoHandler\x00")
				} else {
					w.WriteString("SoundHandler\x00")
				}
			})
			w.box("minf", func() {
				if t.Kind == Video {
					w.fullBox("vmhd", 0, 1, func() { w.zeros(8) })
				} else {
					w.fullBox("smhd", 0, 0, func() { w.zeros(4) })
				}
				w.box("dinf", func() {
					w.fullBox("dref", 0, 0, func() {
						w.u32(1)
						w.fullBox("url ", 0, 1, func() {})
					})
				})
				w.box("stbl", func() { writeStbl(w, t, ti, runs, offsets, co64) })
			})
		})
	})
}

func writeStbl(w *boxWriter, t *Track, ti int, runs []run, offsets []int64, co64 bool) {
	w.Write(t.stsd)

	type entry struct{ count, value uint32 }
	rle := func(n int, get func(int) uint32) []entry {
		var out []entry
		for i := 0; i < n; i++ {
			v := get(i)
			if len(out) > 0 && out[len(out)-1].value == v {
				out[len(out)-1].count++
			} else {
				out = append(out, entry{1, v})
			}
		}
		return out
	}
	n := len(t.Samples)

	stts := rle(n, func(i int) uint32 { return t.Samples[i].Dur })
	w.fullBox("stts", 0, 0, func() {
		w.u32(uint32(len(stts)))
		for _, e := range stts {
			w.u32(e.count)
			w.u32(e.value)
		}
	})

	hasCTO, negCTO := false, false
	for _, s := range t.Samples {
		hasCTO = hasCTO || s.CTO != 0
		negCTO = negCTO || s.CTO < 0
	}
	if hasCTO {
		ctts := rle(n, func(i int) uint32 { return uint32(t.Samples[i].CTO) })
		var v uint8
		if negCTO {
			v = 1
		}
		w.fullBox("ctts", v, 0, func() {
			w.u32(uint32(len(ctts)))
			for _, e := range ctts {
				w.u32(e.count)
				w.u32(e.value)
			}
		})
	}

	var sync []uint32
	for i, s := range t.Samples {
		if s.Key {
			sync = append(sync, uint32(i+1))
		}
	}
	if len(sync) < n {
		w.fullBox("stss", 0, 0, func() {
			w.u32(uint32(len(sync)))
			for _, s := range sync {
				w.u32(s)
			}
		})
	}

	// Chunks of this track, in file order.
	var chunks []int
	for i, r := range runs {
		if r.track == ti {
			chunks = append(chunks, i)
		}
	}
	w.fullBox("stsc", 0, 0, func() {
		type e struct{ first, per uint32 }
		var list []e
		for ci, ri := range chunks {
			per := uint32(runs[ri].count)
			if len(list) == 0 || list[len(list)-1].per != per {
				list = append(list, e{uint32(ci + 1), per})
			}
		}
		w.u32(uint32(len(list)))
		for _, x := range list {
			w.u32(x.first)
			w.u32(x.per)
			w.u32(1)
		}
	})

	w.fullBox("stsz", 0, 0, func() {
		w.u32(0)
		w.u32(uint32(n))
		for _, s := range t.Samples {
			w.u32(s.Size)
		}
	})

	off := func(ri int) int64 {
		if offsets == nil {
			return 0
		}
		return offsets[ri]
	}
	if co64 {
		w.fullBox("co64", 0, 0, func() {
			w.u32(uint32(len(chunks)))
			for _, ri := range chunks {
				w.u64(uint64(off(ri)))
			}
		})
	} else {
		w.fullBox("stco", 0, 0, func() {
			w.u32(uint32(len(chunks)))
			for _, ri := range chunks {
				w.u32(uint32(off(ri)))
			}
		})
	}
}

// writeUdta writes iTunes-style metadata, read by QuickTime, Finder and most players.
func writeUdta(w *boxWriter, meta Meta) {
	tags := []struct{ key, val string }{{"\xa9nam", meta.Title}, {"\xa9ART", meta.Artist}, {"\xa9too", "ytd"}}
	w.box("udta", func() {
		w.fullBox("meta", 0, 0, func() {
			w.fullBox("hdlr", 0, 0, func() {
				w.u32(0)
				w.WriteString("mdirappl")
				w.zeros(9)
			})
			w.box("ilst", func() {
				for _, tag := range tags {
					if tag.val == "" {
						continue
					}
					w.box(tag.key, func() {
						w.box("data", func() {
							w.u32(1) // UTF-8
							w.u32(0)
							w.WriteString(tag.val)
						})
					})
				}
			})
		})
	})
}
