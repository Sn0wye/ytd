package media

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

var oggCRC = func() (t [256]uint32) {
	for i := range t {
		r := uint32(i) << 24
		for range 8 {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04C11DB7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return
}()

type oggWriter struct {
	w      io.Writer
	serial uint32
	seq    uint32
}

// page writes one Ogg page holding whole packets.
func (o *oggWriter) page(packets [][]byte, granule int64, flags byte) error {
	var lacing []byte
	for _, p := range packets {
		n := len(p)
		for n >= 255 {
			lacing = append(lacing, 255)
			n -= 255
		}
		lacing = append(lacing, byte(n))
	}
	if len(lacing) > 255 {
		return errors.New("ogg: page too large")
	}
	var h bytes.Buffer
	h.WriteString("OggS")
	h.WriteByte(0)
	h.WriteByte(flags)
	binary.Write(&h, binary.LittleEndian, granule)
	binary.Write(&h, binary.LittleEndian, o.serial)
	binary.Write(&h, binary.LittleEndian, o.seq)
	h.Write([]byte{0, 0, 0, 0}) // CRC placeholder
	h.WriteByte(byte(len(lacing)))
	h.Write(lacing)
	for _, p := range packets {
		h.Write(p)
	}
	b := h.Bytes()
	var crc uint32
	for _, c := range b {
		crc = crc<<8 ^ oggCRC[byte(crc>>24)^c]
	}
	binary.LittleEndian.PutUint32(b[22:], crc)
	o.seq++
	_, err := o.w.Write(b)
	return err
}

// opusSamples returns the duration of an Opus packet in 48 kHz samples (RFC 6716 §3.1).
func opusSamples(p []byte) int64 {
	if len(p) == 0 {
		return 0
	}
	toc := p[0]
	cfg := toc >> 3
	var frame int64 // in 48 kHz samples
	switch {
	case cfg < 12: // SILK: 10, 20, 40, 60 ms
		frame = []int64{480, 960, 1920, 2880}[cfg%4]
	case cfg < 16: // Hybrid: 10, 20 ms
		frame = []int64{480, 960}[cfg%2]
	default: // CELT: 2.5, 5, 10, 20 ms
		frame = []int64{120, 240, 480, 960}[cfg%4]
	}
	switch toc & 3 {
	case 0:
		return frame
	case 1, 2:
		return 2 * frame
	default:
		if len(p) < 2 {
			return 0
		}
		return int64(p[1]&0x3f) * frame
	}
}

// WriteOggOpus writes an Opus track (from WebM) as a standard .opus file.
func WriteOggOpus(out *os.File, t *Track, meta Meta) error {
	if t.Codec != "opus" || len(t.CodecPrivate) < 19 {
		return errors.New("opus: track is not Opus or lacks OpusHead")
	}
	var serial [4]byte
	rand.Read(serial[:])
	w := bufio.NewWriterSize(out, 1<<20)
	o := &oggWriter{w: w, serial: binary.LittleEndian.Uint32(serial[:])}

	if err := o.page([][]byte{t.CodecPrivate}, 0, 0x02); err != nil {
		return err
	}
	var tags bytes.Buffer
	tags.WriteString("OpusTags")
	vendor := "ytd"
	binary.Write(&tags, binary.LittleEndian, uint32(len(vendor)))
	tags.WriteString(vendor)
	var comments []string
	if meta.Title != "" {
		comments = append(comments, "TITLE="+meta.Title)
	}
	if meta.Artist != "" {
		comments = append(comments, "ARTIST="+meta.Artist)
	}
	binary.Write(&tags, binary.LittleEndian, uint32(len(comments)))
	for _, c := range comments {
		binary.Write(&tags, binary.LittleEndian, uint32(len(c)))
		tags.WriteString(c)
	}
	if tags.Len() > 255*255 {
		return errors.New("opus: tags too long")
	}
	if err := o.page([][]byte{tags.Bytes()}, 0, 0); err != nil {
		return err
	}

	// Audio pages of about one second each.
	var granule int64
	var pagePackets [][]byte
	lacing := 0
	flush := func(last bool) error {
		if len(pagePackets) == 0 {
			return nil
		}
		var flags byte
		if last {
			flags = 0x04
		}
		err := o.page(pagePackets, granule, flags)
		pagePackets, lacing = nil, 0
		return err
	}
	for i, s := range t.Samples {
		p := make([]byte, s.Size)
		if _, err := t.Src.ReadAt(p, s.Offset); err != nil {
			return err
		}
		need := int(s.Size)/255 + 1
		if lacing+need > 255 || len(pagePackets) >= 50 {
			if err := flush(false); err != nil {
				return err
			}
		}
		granule += opusSamples(p)
		if s.Discard > 0 {
			granule -= (s.Discard*48000 + 500_000_000) / 1_000_000_000
		}
		pagePackets = append(pagePackets, p)
		lacing += need
		if i == len(t.Samples)-1 {
			if err := flush(true); err != nil {
				return err
			}
		}
	}
	return w.Flush()
}
