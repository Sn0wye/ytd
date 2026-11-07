package media

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sort"
)

// ebmlWriter builds EBML elements in memory.
type ebmlWriter struct{ bytes.Buffer }

func (w *ebmlWriter) id(id uint32) {
	switch {
	case id >= 1<<24:
		w.Write([]byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)})
	case id >= 1<<16:
		w.Write([]byte{byte(id >> 16), byte(id >> 8), byte(id)})
	case id >= 1<<8:
		w.Write([]byte{byte(id >> 8), byte(id)})
	default:
		w.WriteByte(byte(id))
	}
}

// size writes v as the shortest EBML vint.
func (w *ebmlWriter) size(v uint64) {
	n := 1
	for n < 8 && v >= (1<<(7*n))-1 {
		n++
	}
	w.sizeN(v, n)
}

func (w *ebmlWriter) sizeN(v uint64, n int) {
	b := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	b[0] |= 0x80 >> (n - 1)
	w.Write(b)
}

func (w *ebmlWriter) bin(id uint32, b []byte) {
	w.id(id)
	w.size(uint64(len(b)))
	w.Write(b)
}

func (w *ebmlWriter) str(id uint32, s string) { w.bin(id, []byte(s)) }

func (w *ebmlWriter) uint(id uint32, v uint64) {
	n := 1
	for n < 8 && v>>(8*n) != 0 {
		n++
	}
	b := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	w.bin(id, b)
}

func (w *ebmlWriter) sint(id uint32, v int64) {
	n := 1
	for n < 8 && (v < -(1<<(8*n-1)) || v >= 1<<(8*n-1)) {
		n++
	}
	b := make([]byte, n)
	u := uint64(v)
	for i := n - 1; i >= 0; i-- {
		b[i] = byte(u)
		u >>= 8
	}
	w.bin(id, b)
}

func (w *ebmlWriter) float(id uint32, f float64) {
	w.bin(id, binary.BigEndian.AppendUint64(nil, math.Float64bits(f)))
}

func (w *ebmlWriter) master(id uint32, body func(*ebmlWriter)) {
	var c ebmlWriter
	body(&c)
	w.bin(id, c.Bytes())
}

const (
	idEBMLVersion        = 0x4286
	idEBMLReadVersion    = 0x42F7
	idEBMLMaxIDLength    = 0x42F2
	idEBMLMaxSizeLength  = 0x42F3
	idDocType            = 0x4282
	idDocTypeVersion     = 0x4287
	idDocTypeReadVersion = 0x4285
	idSeek               = 0x4DBB
	idSeekID             = 0x53AB
	idSeekPosition       = 0x53AC
	idMuxingApp          = 0x4D80
	idWritingApp         = 0x5741
	idDuration           = 0x4489
	idTitle              = 0x7BA9
	idTrackUID           = 0x73C5
	idFlagLacing         = 0x9C
	idLanguage           = 0x22B59C
	idCuePoint           = 0xBB
	idCueTime            = 0xB3
	idCueTrackPositions  = 0xB7
	idCueTrack           = 0xF7
	idCueClusterPosition = 0xF1
	idTag                = 0x7373
	idTargets            = 0x63C0
	idTargetTypeValue    = 0x68CA
	idSimpleTag          = 0x67C8
	idTagName            = 0x45A3
	idTagString          = 0x4487
)

var mkvCodecIDs = map[string]string{
	"h264": "V_MPEG4/ISO/AVC", "av1": "V_AV1", "vp9": "V_VP9",
	"aac": "A_AAC", "opus": "A_OPUS",
}

// block is one frame placed in the output.
type block struct {
	track int
	idx   int
	pts   int64 // ms
}

type cluster struct {
	tc     int64
	blocks []block
	cue    bool // starts with a video keyframe
}

// WriteMatroska writes a Matroska (or WebM when webm is true) file holding
// the given tracks, copying the coded frames unchanged.
func WriteMatroska(out *os.File, tracks []*Track, meta Meta, webm bool) error {
	for _, t := range tracks {
		if _, ok := mkvCodecIDs[t.Codec]; !ok {
			return fmt.Errorf("mkv: unsupported codec %q", t.Codec)
		}
		if webm && t.Codec != "vp9" && t.Codec != "av1" && t.Codec != "opus" {
			return fmt.Errorf("webm cannot hold %s; use mkv", t.Codec)
		}
	}

	clusters := planClusters(tracks)

	var durationMS float64
	for _, t := range tracks {
		durationMS = math.Max(durationMS, float64(scale(t.presentationEnd(), 1000, int64(t.Timescale))))
	}

	var head ebmlWriter
	head.master(idEBML, func(w *ebmlWriter) {
		w.uint(idEBMLVersion, 1)
		w.uint(idEBMLReadVersion, 1)
		w.uint(idEBMLMaxIDLength, 4)
		w.uint(idEBMLMaxSizeLength, 8)
		if webm {
			w.str(idDocType, "webm")
		} else {
			w.str(idDocType, "matroska")
		}
		w.uint(idDocTypeVersion, 4)
		w.uint(idDocTypeReadVersion, 2)
	})
	head.id(idSegment)
	segSizePos := head.Len()
	head.sizeN(0, 8) // patched at the end

	info := element(func(w *ebmlWriter) {
		w.master(idInfo, func(w *ebmlWriter) {
			w.uint(idTimecodeScale, 1_000_000)
			w.str(idMuxingApp, "ytd")
			w.str(idWritingApp, "ytd")
			w.float(idDuration, durationMS)
			if meta.Title != "" {
				w.str(idTitle, meta.Title)
			}
		})
	})
	tracksEl := element(func(w *ebmlWriter) {
		w.master(idTracks, func(w *ebmlWriter) {
			for i, t := range tracks {
				writeTrackEntry(w, t, i+1)
			}
		})
	})
	tags := element(func(w *ebmlWriter) {
		w.master(idTags, func(w *ebmlWriter) {
			w.master(idTag, func(w *ebmlWriter) {
				w.master(idTargets, func(w *ebmlWriter) { w.uint(idTargetTypeValue, 50) })
				for _, kv := range [][2]string{{"TITLE", meta.Title}, {"ARTIST", meta.Artist}, {"ENCODER", "ytd"}} {
					if kv[1] == "" {
						continue
					}
					w.master(idSimpleTag, func(w *ebmlWriter) {
						w.str(idTagName, kv[0])
						w.str(idTagString, kv[1])
					})
				}
			})
		})
	})

	// SeekHead with fixed-width positions so the Cues entry can be patched.
	seekEntry := func(w *ebmlWriter, id uint32, pos int64) {
		w.master(idSeek, func(w *ebmlWriter) {
			var idb ebmlWriter
			idb.id(id)
			w.bin(idSeekID, idb.Bytes())
			w.bin(idSeekPosition, binary.BigEndian.AppendUint64(nil, uint64(pos)))
		})
	}
	seekLen := len(element(func(w *ebmlWriter) {
		w.master(idSeekHead, func(w *ebmlWriter) {
			for range 4 {
				seekEntry(w, idInfo, 0)
			}
		})
	}))
	infoPos := int64(seekLen)
	tracksPos := infoPos + int64(len(info))
	tagsPos := tracksPos + int64(len(tracksEl))
	clustersPos := tagsPos + int64(len(tags))

	// Lay out clusters to know the Cues position up front.
	var clusterEls [][]byte // headers only
	clusterPos := make([]int64, len(clusters))
	pos := clustersPos
	for i, c := range clusters {
		hdr, total := clusterHeader(tracks, c)
		clusterEls = append(clusterEls, hdr)
		clusterPos[i] = pos
		pos += total
	}
	cuesPos := pos

	seekHead := element(func(w *ebmlWriter) {
		w.master(idSeekHead, func(w *ebmlWriter) {
			seekEntry(w, idInfo, infoPos)
			seekEntry(w, idTracks, tracksPos)
			seekEntry(w, idTags, tagsPos)
			seekEntry(w, idCues, cuesPos)
		})
	})

	cueTrack := 1
	for i, t := range tracks {
		if t.Kind == Video {
			cueTrack = i + 1
			break
		}
	}
	cues := element(func(w *ebmlWriter) {
		w.master(idCues, func(w *ebmlWriter) {
			for i, c := range clusters {
				if !c.cue {
					continue
				}
				w.master(idCuePoint, func(w *ebmlWriter) {
					w.uint(idCueTime, uint64(c.tc))
					w.master(idCueTrackPositions, func(w *ebmlWriter) {
						w.uint(idCueTrack, uint64(cueTrack))
						w.uint(idCueClusterPosition, uint64(clusterPos[i]))
					})
				})
			}
		})
	})
	segSize := cuesPos + int64(len(cues))
	hb := head.Bytes()
	var sz ebmlWriter
	sz.sizeN(uint64(segSize), 8)
	copy(hb[segSizePos:], sz.Bytes())

	w := bufio.NewWriterSize(out, 4<<20)
	for _, b := range [][]byte{hb, seekHead, info, tracksEl, tags} {
		w.Write(b)
	}
	var bb ebmlWriter
	for i, c := range clusters {
		w.Write(clusterEls[i])
		for _, b := range c.blocks {
			t := tracks[b.track]
			s := t.Samples[b.idx]
			bb.Reset()
			writeBlockHeader(&bb, b, s, c.tc)
			w.Write(bb.Bytes())
			if err := copySamples(w, t.Src, []Sample{s}); err != nil {
				return err
			}
		}
	}
	w.Write(cues)
	return w.Flush()
}

func element(fn func(*ebmlWriter)) []byte {
	var w ebmlWriter
	fn(&w)
	return w.Bytes()
}

func writeTrackEntry(w *ebmlWriter, t *Track, num int) {
	w.master(idTrackEntry, func(w *ebmlWriter) {
		w.uint(idTrackNumber, uint64(num))
		var uid [8]byte
		rand.Read(uid[:])
		w.uint(idTrackUID, binary.BigEndian.Uint64(uid[:])>>1|1)
		if t.Kind == Video {
			w.uint(idTrackType, 1)
		} else {
			w.uint(idTrackType, 2)
		}
		w.uint(idFlagLacing, 0)
		w.str(idLanguage, "und")
		w.str(idCodecID, mkvCodecIDs[t.Codec])
		if len(t.CodecPrivate) > 0 {
			w.bin(idCodecPrivate, t.CodecPrivate)
		}
		if t.CodecDelay > 0 {
			w.uint(idCodecDelay, t.CodecDelay)
		}
		if t.SeekPreRoll > 0 {
			w.uint(idSeekPreRoll, t.SeekPreRoll)
		}
		if d := frameDuration(t); d > 0 {
			w.uint(idDefaultDuration, d)
		}
		if t.Kind == Video {
			w.master(idVideo, func(w *ebmlWriter) {
				w.uint(idPixelWidth, uint64(t.Width))
				w.uint(idPixelHeight, uint64(t.Height))
				w.Write(t.colour)
			})
		} else {
			w.master(idAudio, func(w *ebmlWriter) {
				w.float(idSamplingFreq, t.SampleRate)
				w.uint(idChannels, uint64(max(t.Channels, 1)))
			})
		}
	})
}

// frameDuration returns the frame duration in ns for constant frame rate
// video, or 0.
func frameDuration(t *Track) uint64 {
	if t.Kind != Video || len(t.Samples) < 2 {
		return 0
	}
	if t.defaultDuration > 0 {
		return t.defaultDuration
	}
	d := t.Samples[0].Dur
	for _, s := range t.Samples[:len(t.Samples)-1] {
		if s.Dur != d {
			return 0
		}
	}
	return uint64(scale(int64(d), 1e9, int64(t.Timescale)))
}

// planClusters groups frames into clusters that start on video keyframes,
// roughly every 2-5 seconds, in decode order across tracks.
func planClusters(tracks []*Track) []cluster {
	type item struct {
		block
		dts float64
		key bool
	}
	var items []item
	hasVideo := false
	for ti, t := range tracks {
		hasVideo = hasVideo || t.Kind == Video
		for i, s := range t.Samples {
			items = append(items, item{
				block: block{track: ti, idx: i, pts: int64(math.Round(float64(t.ptsNS(i)) / 1e6))},
				dts:   t.Seconds(s.DTS - t.MediaTime),
				key:   t.Kind == Video && s.Key,
			})
		}
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].dts < items[b].dts })

	var out []cluster
	for _, it := range items {
		var c *cluster
		if len(out) > 0 {
			c = &out[len(out)-1]
		}
		age := int64(0)
		if c != nil {
			age = it.pts - c.tc
		}
		newCluster := c == nil ||
			(hasVideo && it.key && age >= 2000) ||
			(!hasVideo && age >= 5000) ||
			age > 30000 || age < -30000
		if newCluster {
			out = append(out, cluster{tc: max(it.pts, 0), cue: !hasVideo || it.key})
			c = &out[len(out)-1]
		}
		c.blocks = append(c.blocks, it.block)
	}
	return out
}

func blockPayloadLen(s Sample) int { return 4 + int(s.Size) } // track(1) + tc(2) + flags(1) + data

func blockElemLen(s Sample) int {
	var w ebmlWriter
	n := blockPayloadLen(s)
	if s.Discard != 0 {
		var g ebmlWriter
		g.id(idBlock)
		g.size(uint64(n))
		inner := g.Len() + n
		var d ebmlWriter
		d.sint(idDiscardPadding, s.Discard)
		inner += d.Len()
		w.id(idBlockGroup)
		w.size(uint64(inner))
		return w.Len() + inner
	}
	w.id(idSimpleBlock)
	w.size(uint64(n))
	return w.Len() + n
}

// clusterHeader returns the cluster's ID, size and Timecode bytes, and the
// total element length including all blocks.
func clusterHeader(tracks []*Track, c cluster) ([]byte, int64) {
	var body ebmlWriter
	body.uint(idTimecode, uint64(c.tc))
	size := int64(body.Len())
	for _, b := range c.blocks {
		size += int64(blockElemLen(tracks[b.track].Samples[b.idx]))
	}
	var w ebmlWriter
	w.id(idCluster)
	w.size(uint64(size))
	hdrLen := int64(w.Len())
	w.Write(body.Bytes())
	return w.Bytes(), hdrLen + size
}

// writeBlockHeader writes everything of a block element except the frame data.
func writeBlockHeader(w *ebmlWriter, b block, s Sample, clusterTC int64) {
	rel := b.pts - clusterTC
	n := blockPayloadLen(s)
	hdr := []byte{0x80 | byte(b.track+1), byte(uint16(int16(rel)) >> 8), byte(int16(rel)), 0}
	if s.Discard != 0 {
		var g ebmlWriter
		g.id(idBlock)
		g.size(uint64(n))
		var d ebmlWriter
		d.sint(idDiscardPadding, s.Discard)
		w.id(idBlockGroup)
		w.size(uint64(g.Len() + n + d.Len()))
		// DiscardPadding first so the frame data can follow directly.
		w.Write(d.Bytes())
		w.Write(g.Bytes())
		w.Write(hdr)
		return
	}
	if s.Key {
		hdr[3] = 0x80
	}
	w.id(idSimpleBlock)
	w.size(uint64(n))
	w.Write(hdr)
}
