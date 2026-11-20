package ui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// pickerRows is how many rows the picker shows before it scrolls.
const pickerRows = 12

// CanPrompt reports whether the pickers can run: both stdin and stderr are
// terminals.
func (u *UI) CanPrompt() bool {
	return u.tty && term.IsTerminal(os.Stdin.Fd())
}

// Select shows an arrow-key picker under label. note (optional) is a hint
// line, header the column titles above rows. The choice stays on the trail
// as a done step.
func (u *UI) Select(label, note, header string, rows []string) (int, error) {
	if !u.CanPrompt() {
		return 0, errors.New("no terminal to show the quality picker; use --best or --itag")
	}
	if len(rows) == 0 {
		return 0, errors.New("nothing to pick from")
	}

	l := &list{u: u, header: header, rows: rows, indent: 2}
	l.line = func(i int) string {
		if i == l.cur {
			return u.st.accent.Render("●") + " " + rows[i]
		}
		return u.st.dim.Render("○ " + rows[i])
	}
	err := u.prompt(label, note, "↑↓ · enter", l, func(k string) (bool, error) {
		next, chosen, err := handleKey(k, l.cur, len(rows), l.size())
		l.cur = next
		return chosen, err
	})
	if err != nil {
		return 0, err
	}
	u.Picked(label, rows[l.cur])
	return l.cur, nil
}

// MultiSelect shows a checklist under label with every row selected, and
// returns the indexes the user kept, in order. done is the summary left on
// the trail, e.g. "12 of 40 videos".
func (u *UI) MultiSelect(label, header string, rows []string, done func(n int) string) ([]int, error) {
	if !u.CanPrompt() {
		return nil, errors.New("no terminal to show the picker")
	}
	if len(rows) == 0 {
		return nil, errors.New("nothing to pick from")
	}

	sel := make([]bool, len(rows))
	for i := range sel {
		sel[i] = true
	}
	l := &list{u: u, header: header, rows: rows, indent: 4}
	l.line = func(i int) string {
		cursor := " "
		if i == l.cur {
			cursor = u.st.accent.Render("›")
		}
		if sel[i] {
			return cursor + " " + u.st.accent.Render("◼") + " " + rows[i]
		}
		return cursor + " " + u.st.dim.Render("◻ "+rows[i])
	}
	l.footer = func() string {
		return fmt.Sprintf("%d of %d selected", count(sel), len(rows))
	}
	hint := "↑↓ · space toggle · a all/none · i invert · enter"
	err := u.prompt(label, "", hint, l, func(k string) (bool, error) {
		next, done, err := multiKey(k, l.cur, l.size(), sel)
		l.cur = next
		return done, err
	})
	if err != nil {
		return nil, err
	}

	var picked []int
	for i, s := range sel {
		if s {
			picked = append(picked, i)
		}
	}
	u.Picked(label, done(len(picked)))
	return picked, nil
}

// prompt prints label and its hints, runs l in raw mode until handle says
// done, then erases the prompt. On error the label stays as a dim step.
func (u *UI) prompt(label, note, hint string, l *list, handle func(k string) (done bool, err error)) error {
	u.gap()
	top := 1
	u.println(u.st.accent.Render(glyphActive), "  ", label, "  ", u.st.dim.Render(hint))
	if note != "" {
		u.println(u.rail(), "  ", u.st.dim.Render(note))
		top++
	}

	in := os.Stdin.Fd()
	old, err := term.MakeRaw(in)
	if err != nil {
		return err
	}
	err = l.run(handle)
	term.Restore(in, old)

	// Replace the prompt with the result.
	fmt.Fprintf(u.w, "\x1b[%dF\x1b[J", top)
	if err != nil {
		u.println(u.st.dim.Render(glyphDone + "  " + label))
		u.gapNext = true
	}
	return err
}

// list is the scrolling list both pickers draw in raw mode (so lines end in
// \r\n). line renders row i after the rail; footer, if set, adds a dim
// status after the rows.
type list struct {
	u      *UI
	header string
	rows   []string
	indent int // columns line puts before the row text, to align header
	line   func(i int) string
	footer func() string

	cur, top, drawn int
}

func (l *list) size() int { return min(len(l.rows), pickerRows) }

func (l *list) draw() {
	u := l.u
	w := u.width() - 1
	lines := []string{u.rail() + "  " + strings.Repeat(" ", l.indent) + u.st.dim.Render(strings.TrimLeft(l.header, " "))}
	for i := l.top; i < l.top+l.size(); i++ {
		lines = append(lines, u.rail()+"  "+l.line(i))
	}
	var status []string
	if below := len(l.rows) - l.top - l.size(); below > 0 {
		status = append(status, fmt.Sprintf("… %d more", below))
	}
	if l.footer != nil {
		status = append(status, l.footer())
	}
	if len(status) > 0 {
		lines = append(lines, u.rail()+"  "+strings.Repeat(" ", l.indent)+u.st.dim.Render(strings.Join(status, " · ")))
	}

	var b strings.Builder
	if l.drawn > 0 {
		fmt.Fprintf(&b, "\x1b[%dF", l.drawn)
	}
	for _, line := range lines {
		b.WriteString("\x1b[2K" + ansi.Truncate(line, w, "…") + "\r\n")
	}
	// The "more" line can disappear when scrolling to the end.
	for range l.drawn - len(lines) {
		b.WriteString("\x1b[2K\r\n")
	}
	if extra := l.drawn - len(lines); extra > 0 {
		fmt.Fprintf(&b, "\x1b[%dF", extra)
	}
	l.drawn = len(lines)
	fmt.Fprint(u.w, b.String())
}

// run draws the list and feeds key presses to handle, redrawing after each
// read, until handle says done or fails. The list is erased on return.
func (l *list) run(handle func(k string) (done bool, err error)) error {
	w := l.u.w
	fmt.Fprint(w, "\x1b[?25l")
	defer func() { fmt.Fprintf(w, "\x1b[%dF\x1b[J\x1b[?25h", l.drawn) }()
	l.draw()

	buf := make([]byte, 256)
	var pending []byte // an escape sequence cut off by the previous read
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return ErrAborted
		}
		// Key repeat can deliver several presses in one read.
		var keys []string
		keys, pending = splitKeys(append(pending, buf[:n]...))
		for _, k := range keys {
			if done, err := handle(k); err != nil || done {
				return err
			}
		}
		l.top = min(max(l.top, l.cur-l.size()+1), l.cur)
		l.draw()
	}
}

// splitKeys cuts raw input into key presses: CSI/SS3 sequences like
// "\x1b[A", "\x1bOA" or "\x1b[5~", a lone escape, or one UTF-8 character.
// An escape sequence cut off at the end is returned as rest.
func splitKeys(b []byte) (keys []string, rest []byte) {
	for len(b) > 0 {
		n := 1
		switch {
		case b[0] == 0x1b && len(b) >= 2 && (b[1] == '[' || b[1] == 'O'):
			// The sequence ends at the first byte from @ to ~ after the introducer.
			n = 2
			for n < len(b) && (b[n] < '@' || b[n] > '~') {
				n++
			}
			if n == len(b) {
				return keys, b
			}
			n++
		case b[0] >= 0x80:
			_, n = utf8.DecodeRune(b)
		}
		keys = append(keys, string(b[:n]))
		b = b[n:]
	}
	return keys, nil
}

// move maps a navigation key to the new cursor in a list of n rows showing
// page rows at a time. Up and down wrap around; page keys stop at the ends.
func move(k string, cur, n, page int) (next int, ok bool) {
	switch k {
	case "\x1b[A", "\x1bOA", "k", "\x10": // up, ctrl-p
		return (cur - 1 + n) % n, true
	case "\x1b[B", "\x1bOB", "j", "\x0e": // down, ctrl-n
		return (cur + 1) % n, true
	case "\x1b[5~": // page up
		return max(cur-page, 0), true
	case "\x1b[6~": // page down
		return min(cur+page, n-1), true
	case "\x1b[H", "\x1bOH", "g": // home
		return 0, true
	case "\x1b[F", "\x1bOF", "G": // end
		return n - 1, true
	}
	return cur, false
}

func isAbort(k string) bool {
	switch k {
	case "\x03", "\x04", "\x1b", "q": // ctrl-c, ctrl-d, esc
		return true
	}
	return false
}

// handleKey maps one key press of the single picker to the new cursor, a
// choice, or ErrAborted.
func handleKey(k string, cur, n, page int) (next int, chosen bool, err error) {
	if next, ok := move(k, cur, n, page); ok {
		return next, false, nil
	}
	switch {
	case k == "\r" || k == "\n" || k == " ":
		return cur, true, nil
	case isAbort(k):
		return cur, false, ErrAborted
	}
	return cur, false, nil
}

// multiKey maps one key press of the checklist to the new cursor, updating
// sel in place. Enter confirms only when something is selected.
func multiKey(k string, cur, page int, sel []bool) (next int, done bool, err error) {
	if next, ok := move(k, cur, len(sel), page); ok {
		return next, false, nil
	}
	switch {
	case k == " " || k == "x":
		sel[cur] = !sel[cur]
	case k == "a" || k == "A":
		all := count(sel) < len(sel) // select all, or clear when all are selected
		for i := range sel {
			sel[i] = all
		}
	case k == "i" || k == "I":
		for i := range sel {
			sel[i] = !sel[i]
		}
	case k == "\r" || k == "\n":
		return cur, count(sel) > 0, nil
	case isAbort(k):
		return cur, false, ErrAborted
	}
	return cur, false, nil
}

func count(sel []bool) int {
	n := 0
	for _, s := range sel {
		if s {
			n++
		}
	}
	return n
}
