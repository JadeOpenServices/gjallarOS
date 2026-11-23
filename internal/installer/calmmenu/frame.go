package calmmenu

import "fmt"

// Frame is the part of the screen a page uses: the whole screen less the
// same margin on every side. Pages are drawn in it, running output wraps
// at its width, and the terminal scrolls only inside it, so text never
// runs into the margin at the bottom either.
type Frame struct {
	Col, Row      int // first column and row, from 1
	Width, Height int // in cells
}

// FrameFor is the frame of a terminal cols by rows cells. The margin is
// a sixteenth of the rows, at least one. A cell is taken as twice as tall
// as it is wide, as on the Linux console, which does not report its
// pixel size: the sides get two columns for each row of margin, so the
// margin looks the same on all four sides. A terminal too small for a
// margin gets none.
func FrameFor(cols, rows int) Frame {
	m := max(1, rows/16)
	f := Frame{Col: 2*m + 1, Row: m + 1, Width: cols - 4*m, Height: rows - 2*m}
	if f.Width < 20 {
		f.Col, f.Width = 1, max(cols, 0)
	}
	if f.Height < 5 {
		f.Row, f.Height = 1, max(rows, 1)
	}
	return f
}

// scroll sets the terminal's scroll region to the frame's rows. The
// terminal then puts the cursor home; Clear sets the region back to the
// whole screen.
func (f Frame) scroll() string {
	return fmt.Sprintf("\x1b[%d;%dr", f.Row, f.Row+f.Height-1)
}

// bottom is the frame's last row.
func (f Frame) bottom() int { return f.Row + f.Height - 1 }
