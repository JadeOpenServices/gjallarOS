package calmmenu

import "fmt"

// Size is a terminal's size in cells and, when the terminal says, in
// pixels. Pixels are 0 when it does not say, as on the Linux console.
type Size struct {
	Cols, Rows     int
	XPixel, YPixel int
}

// ratio is how many times taller than wide one cell is, as a fraction
// num/den, from the pixel size. A terminal that reports no pixel size,
// or one far off (a cell lower than wide or more than four times as tall
// as wide), gets 2/1: the Linux console's cell is about twice as tall as
// it is wide.
func (s Size) ratio() (num, den int) {
	if s.Cols <= 0 || s.Rows <= 0 || s.XPixel <= 0 || s.YPixel <= 0 {
		return 2, 1
	}
	num, den = s.YPixel*s.Cols, s.XPixel*s.Rows
	if num < den || num > 4*den {
		return 2, 1
	}
	return num, den
}

// across is how many columns are as wide as n rows are tall, rounded.
func (s Size) across(n int) int {
	num, den := s.ratio()
	return (2*n*num + den) / (2 * den)
}

// down is how many rows are as tall as n columns are wide, rounded down.
func (s Size) down(n int) int {
	num, den := s.ratio()
	return n * den / num
}

// Frame is the part of the screen a page uses: the whole screen less a
// margin at every side, at most FrameWidth wide and centred (see
// FrameFor). Pages are drawn in it, running output wraps at its width,
// and the terminal scrolls only inside it, so text never runs into the
// margin at the bottom either.
type Frame struct {
	Col, Row      int // first column and row, from 1
	Width, Height int // in cells
}

// FrameWidth is the widest a page's frame gets, in columns: text on a
// page wraps at 72, or at the frame's width when the screen leaves less.
const FrameWidth = 72

// FrameFor is the frame of a terminal of size s. The margin is a
// sixteenth of the rows, at least one; the sides get as many columns as
// are as wide as that margin is tall (see Size.ratio), so the margin
// looks the same on all four sides. A frame wider than FrameWidth is cut
// to FrameWidth and centred, so the page is never wider than its text; it
// keeps its full height. A terminal too small for a margin gets none.
func FrameFor(s Size) Frame {
	m := max(1, s.Rows/16)
	f := withMargin(s, m, s.across(m))
	if f.Width > FrameWidth {
		f.Col += (f.Width - FrameWidth) / 2
		f.Width = FrameWidth
	}
	if f.Width < 20 {
		f.Col, f.Width = 1, max(s.Cols, 0)
	}
	if f.Height < 5 {
		f.Row, f.Height = 1, max(s.Rows, 1)
	}
	return f
}

// withMargin is the frame of a terminal of size s less m rows at the top
// and bottom and side columns at each side.
func withMargin(s Size, m, side int) Frame {
	return Frame{Col: side + 1, Row: m + 1, Width: s.Cols - 2*side, Height: s.Rows - 2*m}
}

// fitFrame is the frame for a page whose whole size is known, such as a
// menu: FrameFor's frame (at most FrameWidth wide), with the side margin
// grown a column at a time (and the top and bottom margin with it, as
// tall as the sides are wide) for as long as fits says the page still
// fits. A short page then has
// even space around it on every side of any screen, not FrameFor's
// margin with all the rest above and below it.
func fitFrame(s Size, fits func(Frame) bool) Frame {
	f := FrameFor(s)
	if f.Col == 1 || f.Row == 1 {
		return f // no room for a margin at all
	}
	for side := f.Col; ; side++ {
		next := withMargin(s, max(f.Row-1, s.down(side)), side)
		if next.Width < 20 || next.Height < 5 || !fits(next) {
			return f
		}
		f = next
	}
}

// scroll sets the terminal's scroll region to the frame's rows. The
// terminal then puts the cursor home; Clear sets the region back to the
// whole screen.
func (f Frame) scroll() string {
	return fmt.Sprintf("\x1b[%d;%dr", f.Row, f.Row+f.Height-1)
}

// bottom is the frame's last row.
func (f Frame) bottom() int { return f.Row + f.Height - 1 }
