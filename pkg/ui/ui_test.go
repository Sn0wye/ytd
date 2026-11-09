package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// A UI on a buffer is not a terminal: no colours, no animation, no escapes.
func plain() (*UI, *bytes.Buffer) {
	var b bytes.Buffer
	return newUI(&b, false, func() int { return 80 }), &b
}

func TestTrailOffTerminal(t *testing.T) {
	u, b := plain()
	u.Intro("0.2")
	u.Task("Fetching video info", "Video found")(nil)
	u.Info("Big Buck Bunny", "Blender", 10*time.Minute+34*time.Second)
	u.Picked("Video quality", "1080p60        H.264   4300 kbps   325.2 MB   299")
	u.Picked("Audio quality", "AAC      129 kbps stereo       9.7 MB   140")
	p := u.StartProgress("Downloading")
	p.Bytes(50, 100)
	p.Done("Downloaded", "334.9 MB in 6.6s")
	u.Task("Muxing into mp4", "Muxed into mp4")(nil)
	u.Saved("/tmp/x.mp4", 351_000_000, 13800*time.Millisecond)

	want := `┌   ytd   v0.2
│
◇  Video found
│  Big Buck Bunny
│  Blender · 10:34
│
◇  Video quality
│  1080p60 · H.264 · 4300 kbps · 325.2 MB · 299
│
◇  Audio quality
│  AAC · 129 kbps stereo · 9.7 MB · 140
│
◇  Downloaded 334.9 MB in 6.6s
│
◇  Muxed into mp4
│
└  Saved /tmp/x.mp4
   334.7 MB · 13.8s
`
	if got := b.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFailures(t *testing.T) {
	u, b := plain()
	u.Fail(errors.New("unsupported format \"avi\""))
	if got, want := b.String(), "▲  unsupported format \"avi\"\n"; got != want {
		t.Errorf("before intro: got %q, want %q", got, want)
	}

	u, b = plain()
	u.Intro("0.2")
	u.Task("Fetching video info", "Video found")(errors.New("x"))
	u.Fail(errors.New("video unavailable: Private video"))
	want := "┌   ytd   v0.2\n│\n▲  Fetching video info\n│  video unavailable: Private video\n└  Failed\n"
	if got := b.String(); got != want {
		t.Errorf("task failure: got:\n%s\nwant:\n%s", got, want)
	}

	u, b = plain()
	u.Intro("0.2")
	u.Info("Big Buck Bunny", "Blender", time.Minute)
	u.Fail(errors.New("no terminal"))
	want = "┌   ytd   v0.2\n│  Big Buck Bunny\n│  Blender · 1:00\n│\n▲  no terminal\n└  Failed\n"
	if got := b.String(); got != want {
		t.Errorf("failure outside a step: got:\n%s\nwant:\n%s", got, want)
	}

	u, b = plain()
	u.Intro("0.2")
	p := u.StartProgress("Downloading")
	p.Bytes(30, 100)
	p.Fail()
	u.Fail(ErrAborted)
	got := b.String()
	bar := "▲  Downloading\n│  " + strings.Repeat("━", 9) + "╸" + strings.Repeat("━", 20) + "   30%  "
	if !strings.Contains(got, bar) || !strings.HasSuffix(got, " left\n└  Cancelled\n") {
		t.Errorf("cancelled download: got:\n%s", got)
	}
}

func TestSelectNeedsTerminal(t *testing.T) {
	u, b := plain()
	if _, err := u.Select("Video quality", "", "QUALITY", []string{"1080p"}); err == nil || !strings.Contains(err.Error(), "--best") {
		t.Errorf("want an error pointing to --best, got %v", err)
	}
	if b.Len() != 0 {
		t.Errorf("printed %q before failing", b.String())
	}
}

func TestBar(t *testing.T) {
	u, _ := plain()
	for _, c := range []struct {
		frac       float64
		fill, rest int
	}{{0, 0, barWidth - 1}, {0.5, barWidth / 2, barWidth/2 - 1}, {1, barWidth, 0}} {
		p := &Progress{u: u}
		p.set(c.frac, "")
		bar := p.bar()
		if f, r := strings.Count(bar, "━"), strings.Count(bar, "╸"); f != c.fill+c.rest || (r == 1) != (c.frac < 1) {
			t.Errorf("frac %.1f: %q", c.frac, bar)
		}
	}
}

func TestHandleKey(t *testing.T) {
	cases := []struct {
		key    string
		cur    int
		next   int
		chosen bool
		err    error
	}{
		{"\x1b[B", 0, 1, false, nil},
		{"j", 2, 0, false, nil}, // wraps down
		{"\x1b[A", 0, 2, false, nil},
		{"k", 1, 0, false, nil},
		{"G", 0, 2, false, nil},
		{"g", 2, 0, false, nil},
		{"\r", 1, 1, true, nil},
		{"\x03", 1, 1, false, ErrAborted},
		{"\x1b", 1, 1, false, ErrAborted},
		{"x", 1, 1, false, nil},
	}
	for _, c := range cases {
		next, chosen, err := handleKey(c.key, c.cur, 3)
		if next != c.next || chosen != c.chosen || !errors.Is(err, c.err) {
			t.Errorf("handleKey(%q, %d) = %d, %v, %v", c.key, c.cur, next, chosen, err)
		}
	}
}

func TestFormatters(t *testing.T) {
	if got := Compact("  2160p60        AV1    17800 kbps    1.31 GB   401"); got != "2160p60 · AV1 · 17800 kbps · 1.31 GB · 401" {
		t.Errorf("Compact: %q", got)
	}
	for d, want := range map[time.Duration]string{
		34 * time.Second: "0:34", 10*time.Minute + 34*time.Second: "10:34", time.Hour + 2*time.Second: "1:00:02",
	} {
		if got := Clock(d); got != want {
			t.Errorf("Clock(%v) = %q, want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{
		9 * time.Second: "9s", 192 * time.Second: "3m12s", 65 * time.Minute: "1h05m",
	} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestSplitKeys(t *testing.T) {
	keys, rest := splitKeys([]byte("\x1b[B\x1b[Bj\x1bOA\x1b[5~\r\x1b"))
	want := []string{"\x1b[B", "\x1b[B", "j", "\x1bOA", "\x1b[5~", "\r", "\x1b"}
	if strings.Join(keys, "|") != strings.Join(want, "|") || rest != nil {
		t.Errorf("splitKeys = %q, %q; want %q", keys, rest, want)
	}

	// A sequence split across two reads is completed by the second one.
	keys, rest = splitKeys([]byte("j\x1b["))
	if strings.Join(keys, "|") != "j" || string(rest) != "\x1b[" {
		t.Fatalf("first read: %q, rest %q", keys, rest)
	}
	keys, rest = splitKeys(append(rest, 'B'))
	if strings.Join(keys, "|") != "\x1b[B" || rest != nil {
		t.Errorf("second read: %q, rest %q", keys, rest)
	}
}
