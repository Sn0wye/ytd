package media

import (
	"fmt"
	"os"
)

// Mux opens the source files (path + source extension pairs), and writes
// them as one file in container format at out.
func Mux(out, container string, sources []Source, meta Meta) (err error) {
	var tracks []*Track
	for _, src := range sources {
		f, err := os.Open(src.Path)
		if err != nil {
			return err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		t, err := Open(f, st.Size(), src.Ext)
		if err != nil {
			return fmt.Errorf("%s: %w", src.Path, err)
		}
		tracks = append(tracks, t)
	}

	o, err := os.Create(out)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := o.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(out)
		}
	}()

	switch container {
	case "mp4", "m4a":
		return WriteMP4(o, tracks, meta, container == "m4a")
	case "mkv", "webm":
		return WriteMatroska(o, tracks, meta, container == "webm")
	case "opus":
		if len(tracks) != 1 {
			return fmt.Errorf("opus output takes one audio track")
		}
		return WriteOggOpus(o, tracks[0], meta)
	}
	return fmt.Errorf("unsupported output format %q", container)
}

// Source is a downloaded stream file.
type Source struct {
	Path string
	Ext  string // mp4, m4a or webm
}
