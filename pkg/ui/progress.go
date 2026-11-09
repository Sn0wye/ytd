package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Sn0wye/ytd/pkg/utils"
)

const barWidth = 30

// Progress is a step with a live bar. Its update methods may be called from
// any goroutine.
type Progress struct {
	u      *UI
	label  string
	start  time.Time
	live   *live
	mu     sync.Mutex
	frac   float64
	detail string
}

// StartProgress begins a step showing label and a bar at 0%.
func (u *UI) StartProgress(label string) *Progress {
	u.gap()
	p := &Progress{u: u, label: label, start: time.Now()}
	p.live = u.startLive(func(f int) []string {
		return []string{u.st.accent.Render(spinner[f%len(spinner)]) + "  " + label, p.bar()}
	})
	return p
}

func (p *Progress) set(frac float64, detail string) {
	p.mu.Lock()
	p.frac, p.detail = min(max(frac, 0), 1), detail
	p.mu.Unlock()
}

// Bytes reports a download: done of total bytes, with speed and time left.
func (p *Progress) Bytes(done, total int64) {
	el := time.Since(p.start).Seconds()
	speed := float64(done) / max(el, 0.001)
	left := "-"
	if speed > 0 && total > done {
		left = Duration(time.Duration(float64(total-done) / speed * float64(time.Second)))
	}
	p.set(float64(done)/float64(max(total, 1)), fmt.Sprintf("%s/s · %s left", utils.FormatSize(int64(speed)), left))
}

// Media reports a re-encode: done of total media time, with the speed
// relative to real time.
func (p *Progress) Media(done, total time.Duration) {
	speed := done.Seconds() / max(time.Since(p.start).Seconds(), 0.001)
	p.set(done.Seconds()/max(total.Seconds(), 0.001), fmt.Sprintf("%s / %s · %.1fx", Clock(done), Clock(total), speed))
}

func (p *Progress) bar() string {
	p.mu.Lock()
	frac, detail := p.frac, p.detail
	p.mu.Unlock()
	st := p.u.st
	n := int(frac * barWidth)
	head := ""
	if n < barWidth {
		head = st.accent.Render("╸")
	}
	bar := st.accent.Render(strings.Repeat("━", n)) + head + st.track.Render(strings.Repeat("━", max(0, barWidth-n-1)))
	line := p.u.rail() + "  " + bar + "  " + fmt.Sprintf("%3.0f%%", frac*100)
	if detail != "" {
		line += "  " + st.dim.Render(detail)
	}
	return line
}

// Done ends the step as finished, e.g. Done("Downloaded", "312 MB in 8.1s").
func (p *Progress) Done(title, detail string) {
	p.live.stop([]string{p.u.st.ok.Render(glyphDone) + "  " + title + " " + p.u.st.dim.Render(detail)})
	p.u.gapNext = true
}

// Fail ends the step as failed, keeping the bar where it stopped.
func (p *Progress) Fail() {
	p.live.stop([]string{p.u.st.accent.Render(glyphFail) + "  " + p.label, p.bar()})
}

// Elapsed is the time since the step started.
func (p *Progress) Elapsed() time.Duration { return time.Since(p.start) }

// Duration formats a short duration: 45s, 3m12s, 1h05m.
func Duration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
