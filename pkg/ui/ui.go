// Package ui draws ytd's terminal output: a vertical trail of steps with one
// accent colour, live spinners and progress bars, and an arrow-key picker.
//
//	┌  ytd  v0.2
//	│
//	◇  Video found
//	│  Big Buck Bunny
//	│  Blender · 10:34
//	│
//	◆  Video quality
//	│  ● 1080p60  H.264 ...
//	│  ○ 720p60   H.264 ...
//
// Everything goes to stderr. When stderr is not a terminal there are no
// colours, animations or cursor movement: each step prints its final line.
package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Sn0wye/ytd/pkg/utils"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

// ErrAborted is returned when the user cancels a prompt.
var ErrAborted = errors.New("aborted")

const (
	glyphOpen   = "┌"
	glyphClose  = "└"
	glyphRail   = "│"
	glyphDone   = "◇"
	glyphActive = "◆"
	glyphFail   = "▲"
)

var spinner = []string{"◒", "◐", "◓", "◑"}

type styles struct {
	accent, rail, dim, bold, ok, track, badge lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	accent := lipgloss.Color("#ff4d4d")
	return styles{
		accent: r.NewStyle().Foreground(accent),
		rail:   r.NewStyle().Foreground(lipgloss.Color("#4a4a55")),
		dim:    r.NewStyle().Foreground(lipgloss.Color("#8b8b96")),
		bold:   r.NewStyle().Bold(true),
		ok:     r.NewStyle().Foreground(lipgloss.Color("#4ade80")),
		track:  r.NewStyle().Foreground(lipgloss.Color("#33333d")),
		badge:  r.NewStyle().Background(accent).Foreground(lipgloss.Color("#ffffff")).Bold(true).Padding(0, 1),
	}
}

// UI writes the trail. It is not safe for concurrent use, except for the
// Progress it returns.
type UI struct {
	w       io.Writer
	tty     bool
	width   func() int
	st      styles
	started bool // Intro was printed
	gapNext bool // the next step needs a "│" spacer above it
}

// New returns a UI on stderr.
func New() *UI {
	fd := os.Stderr.Fd()
	return newUI(os.Stderr, term.IsTerminal(fd), func() int {
		if w, _, err := term.GetSize(fd); err == nil && w > 0 {
			return w
		}
		return 80
	})
}

func newUI(w io.Writer, tty bool, width func() int) *UI {
	return &UI{w: w, tty: tty, width: width, st: newStyles(lipgloss.NewRenderer(w))}
}

func (u *UI) println(parts ...string) {
	fmt.Fprintln(u.w, strings.Join(parts, ""))
}

func (u *UI) rail() string { return u.st.rail.Render(glyphRail) }

// gap separates the step about to start from the previous one.
func (u *UI) gap() {
	if u.gapNext {
		u.println(u.rail())
	}
	u.gapNext = false
}

// Intro opens the trail.
func (u *UI) Intro(version string) {
	u.started = true
	u.println(u.st.rail.Render(glyphOpen), "  ", u.st.badge.Render("ytd"), "  ", u.st.dim.Render("v"+version))
	u.gapNext = true
}

// Task shows a spinner next to active until the returned func is called. On
// success the step is kept as done; on error it is marked failed.
func (u *UI) Task(active, done string) func(error) {
	u.gap()
	l := u.startLive(func(f int) []string {
		return []string{u.st.accent.Render(spinner[f%len(spinner)]) + "  " + active + u.st.dim.Render(strings.Repeat(".", f/3%4))}
	})
	return func(err error) {
		if err != nil {
			l.stop([]string{u.st.accent.Render(glyphFail) + "  " + active})
			return
		}
		l.stop([]string{u.st.ok.Render(glyphDone) + "  " + done})
		u.gapNext = true
	}
}

// Info adds the video's details under the current step.
func (u *UI) Info(title, author string, d time.Duration) {
	u.Describe(title, author+" · "+Clock(d))
}

// Describe adds a bold title and a dim detail line under the current step.
func (u *UI) Describe(title, detail string) {
	u.println(u.rail(), "  ", u.st.bold.Render(title))
	u.println(u.rail(), "  ", u.st.dim.Render(detail))
	u.gapNext = true
}

// Warn adds an error line under the step that just failed, without closing
// the trail, e.g. for one failed entry of a playlist.
func (u *UI) Warn(msg string) {
	for _, line := range strings.Split(msg, "\n") {
		u.println(u.rail(), "  ", u.st.accent.Render(line))
	}
	u.gapNext = true
}

// Finish closes the trail with a summary: title in green when ok, in the
// accent colour otherwise, and a dim detail line below.
func (u *UI) Finish(ok bool, title, detail string) {
	u.gap()
	style := u.st.ok
	if !ok {
		style = u.st.accent
	}
	u.println(u.st.rail.Render(glyphClose), "  ", style.Render(title))
	if detail != "" {
		for _, line := range strings.Split(detail, "\n") {
			u.println("   ", u.st.dim.Render(line))
		}
	}
}

// Picked records a choice made without the picker (--best, --itag).
func (u *UI) Picked(label, row string) {
	u.gap()
	u.println(u.st.ok.Render(glyphDone), "  ", label)
	u.println(u.rail(), "  ", u.st.dim.Render(Compact(row)))
	u.gapNext = true
}

// Saved closes the trail after a successful download.
func (u *UI) Saved(path string, size int64, took time.Duration) {
	u.gap()
	u.println(u.st.rail.Render(glyphClose), "  ", u.st.ok.Render("Saved "), u.st.bold.Render(ShortPath(path)))
	u.println("   ", u.st.dim.Render(fmt.Sprintf("%s · %.1fs", utils.FormatSize(size), took.Seconds())))
}

// Fail closes the trail with err. ErrAborted reads as a cancellation.
func (u *UI) Fail(err error) {
	if errors.Is(err, ErrAborted) {
		if u.started {
			u.gap()
			u.println(u.st.rail.Render(glyphClose), "  ", u.st.dim.Render("Cancelled"))
		}
		return
	}
	if !u.started {
		u.println(u.st.accent.Render(glyphFail), "  ", u.st.accent.Render(err.Error()))
		return
	}
	lines := strings.Split(err.Error(), "\n")
	// Right after a failed step the error reads as its detail; otherwise it
	// is a step of its own.
	if u.gapNext {
		u.gap()
		u.println(u.st.accent.Render(glyphFail), "  ", u.st.accent.Render(lines[0]))
		lines = lines[1:]
	}
	for _, line := range lines {
		u.println(u.rail(), "  ", u.st.accent.Render(line))
	}
	u.println(u.st.rail.Render(glyphClose), "  ", u.st.accent.Render("Failed"))
}

var multiSpace = regexp.MustCompile(`\s{2,}`)

// Compact turns an aligned table row into "1080p60 · H.264 · 4300 kbps".
func Compact(row string) string {
	return multiSpace.ReplaceAllString(strings.TrimSpace(row), " · ")
}

// ShortPath replaces the home directory with ~.
func ShortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

// Clock formats d as m:ss or h:mm:ss.
func Clock(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
