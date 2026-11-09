package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const frameEvery = 110 * time.Millisecond

// live is a block of lines redrawn in place by a ticker. Off a terminal it
// draws nothing until stop, which prints the final lines once.
type live struct {
	u      *UI
	render func(frame int) []string
	mu     sync.Mutex // guards lines and writes to u.w
	lines  int
	quit   chan struct{}
	done   chan struct{}
}

func (u *UI) startLive(render func(frame int) []string) *live {
	l := &live{u: u, render: render, quit: make(chan struct{}), done: make(chan struct{})}
	if !u.tty {
		close(l.done)
		return l
	}
	fmt.Fprint(u.w, "\x1b[?25l")
	l.draw(render(0))
	go func() {
		defer close(l.done)
		t := time.NewTicker(frameEvery)
		defer t.Stop()
		for f := 1; ; f++ {
			select {
			case <-l.quit:
				return
			case <-t.C:
				l.draw(l.render(f))
			}
		}
	}()
	return l
}

func (l *live) draw(lines []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.u.tty {
		for _, line := range lines {
			fmt.Fprintln(l.u.w, line)
		}
		return
	}
	// Lines are cut to the terminal width: a wrapped line would make the
	// cursor-up below land in the wrong place.
	w := l.u.width() - 1
	var b strings.Builder
	if l.lines > 0 {
		fmt.Fprintf(&b, "\x1b[%dF", l.lines)
	}
	for i := range max(len(lines), l.lines) {
		b.WriteString("\x1b[2K")
		if i < len(lines) {
			b.WriteString(ansi.Truncate(lines[i], w, "…"))
		}
		b.WriteString("\n")
	}
	if extra := l.lines - len(lines); extra > 0 {
		fmt.Fprintf(&b, "\x1b[%dF", extra)
	}
	l.lines = len(lines)
	fmt.Fprint(l.u.w, b.String())
}

// stop ends the animation and leaves final on screen.
func (l *live) stop(final []string) {
	close(l.quit)
	<-l.done
	l.draw(final)
	if l.u.tty {
		fmt.Fprint(l.u.w, "\x1b[?25h")
	}
}
