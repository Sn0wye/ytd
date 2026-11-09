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

// Select shows an arrow-key picker under label. note (optional) is a hint
// line, header the column titles above rows. The choice stays on the trail
// as a done step.
func (u *UI) Select(label, note, header string, rows []string) (int, error) {
	in := os.Stdin.Fd()
	if !u.tty || !term.IsTerminal(in) {
		return 0, errors.New("no terminal to show the quality picker; use --best or --itag")
	}
	if len(rows) == 0 {
		return 0, errors.New("nothing to pick from")
	}

	u.gap()
	top := 1
	u.println(u.st.accent.Render(glyphActive), "  ", label, "  ", u.st.dim.Render("↑↓ · enter"))
	if note != "" {
		u.println(u.rail(), "  ", u.st.dim.Render(note))
		top++
	}

	old, err := term.MakeRaw(in)
	if err != nil {
		return 0, err
	}
	i, err := u.pick(header, rows)
	term.Restore(in, old)

	// Replace the prompt with the result.
	fmt.Fprintf(u.w, "\x1b[%dF\x1b[J", top)
	if err != nil {
		u.println(u.st.dim.Render(glyphDone + "  " + label))
		u.gapNext = true
		return 0, err
	}
	u.Picked(label, rows[i])
	return i, nil
}

// pick runs the list in raw mode (so lines end in \r\n) and erases it when done.
func (u *UI) pick(header string, rows []string) (int, error) {
	size := min(len(rows), pickerRows)
	cur, top, drawn := 0, 0, 0
	draw := func() {
		w := u.width() - 1
		lines := []string{u.rail() + "    " + u.st.dim.Render(strings.TrimLeft(header, " "))}
		for i := top; i < top+size; i++ {
			if i == cur {
				lines = append(lines, u.rail()+"  "+u.st.accent.Render("●")+" "+rows[i])
			} else {
				lines = append(lines, u.rail()+"  "+u.st.dim.Render("○ "+rows[i]))
			}
		}
		if below := len(rows) - top - size; below > 0 {
			lines = append(lines, u.rail()+"    "+u.st.dim.Render(fmt.Sprintf("… %d more", below)))
		}
		var b strings.Builder
		if drawn > 0 {
			fmt.Fprintf(&b, "\x1b[%dF", drawn)
		}
		for _, l := range lines {
			b.WriteString("\x1b[2K" + ansi.Truncate(l, w, "…") + "\r\n")
		}
		// The "more" line can disappear when scrolling to the end.
		for range drawn - len(lines) {
			b.WriteString("\x1b[2K\r\n")
		}
		if extra := drawn - len(lines); extra > 0 {
			fmt.Fprintf(&b, "\x1b[%dF", extra)
		}
		drawn = len(lines)
		fmt.Fprint(u.w, b.String())
	}

	fmt.Fprint(u.w, "\x1b[?25l")
	defer func() { fmt.Fprintf(u.w, "\x1b[%dF\x1b[J\x1b[?25h", drawn) }()
	draw()

	buf := make([]byte, 256)
	var pending []byte // an escape sequence cut off by the previous read
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return 0, ErrAborted
		}
		// Key repeat can deliver several presses in one read.
		var keys []string
		keys, pending = splitKeys(append(pending, buf[:n]...))
		moved := false
		for _, k := range keys {
			next, chosen, err := handleKey(k, cur, len(rows))
			if err != nil || chosen {
				return cur, err
			}
			moved = moved || next != cur
			cur = next
		}
		if moved {
			top = min(max(top, cur-size+1), cur)
			draw()
		}
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

// handleKey maps one key press to the new cursor, a choice, or ErrAborted.
// Up and down wrap around.
func handleKey(k string, cur, n int) (next int, chosen bool, err error) {
	switch k {
	case "\x1b[A", "\x1bOA", "k", "\x10": // up, ctrl-p
		return (cur - 1 + n) % n, false, nil
	case "\x1b[B", "\x1bOB", "j", "\x0e": // down, ctrl-n
		return (cur + 1) % n, false, nil
	case "\x1b[H", "\x1bOH", "g": // home
		return 0, false, nil
	case "\x1b[F", "\x1bOF", "G": // end
		return n - 1, false, nil
	case "\r", "\n", " ":
		return cur, true, nil
	case "\x03", "\x04", "\x1b", "q": // ctrl-c, ctrl-d, esc
		return cur, false, ErrAborted
	}
	return cur, false, nil
}
