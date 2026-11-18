package prompt

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/calmmenu"
	"github.com/JadeOpenServices/gjallarOS/internal/tty"
	"golang.org/x/sys/unix"
)

// Pages for programs the stick starts (THEMEBRAIN section 18): every
// question the installer asks on the stick console ends a page, and the
// next page starts with the step title on a clean screen.
//
// The new page starts right after an answer, not when the next question
// comes: some steps print straight to the terminal (Secure Boot and
// firmware tools), and a page drawn at the question would wipe what they
// printed before the user read it. After an answer the user has read
// everything on the screen.
//
// Page mode is opt-in. The stick sets PageEnv for the programs it starts,
// and pages only show when the output is a terminal too. gjallarctl and
// the installer on an installed system never see the variable.
const (
	PageEnv      = "GJALLAR_PAGES"
	PageTitleEnv = "GJALLAR_PAGE_TITLE"
)

type pageKey struct{}

// WithPage marks ctx: a program started with it shows pages with title on
// top. The stick passes it to the Runner.
func WithPage(ctx context.Context, title string) context.Context {
	return context.WithValue(ctx, pageKey{}, title)
}

// PageEnviron returns env for a program started with ctx: without any
// page variables, and with them when ctx asks for pages.
func PageEnviron(ctx context.Context, env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, e := range env {
		if strings.HasPrefix(e, PageEnv+"=") || strings.HasPrefix(e, PageTitleEnv+"=") {
			continue
		}
		out = append(out, e)
	}
	if title, ok := ctx.Value(pageKey{}).(string); ok {
		out = append(out, PageEnv+"=1", PageTitleEnv+"="+title)
	}
	return out
}

// Pager draws the pages. It sits under the install log's writer, so the
// log gets the text and never the escape codes that place it.
type Pager struct {
	mu    sync.Mutex
	file  *os.File
	title string
	col   *calmmenu.Column
	// secret is set while recovery material is on the screen: the next
	// answer keeps the screen, so the next question shows under it.
	secret bool
}

// Pages turns page mode on when the stick asked for it and out is a
// terminal. errOut gets the same column when it is the same terminal. The
// variables are removed again, so programs the installer starts do not
// draw pages of their own.
func Pages(out, errOut io.Writer) (io.Writer, io.Writer) {
	on := os.Getenv(PageEnv) == "1"
	title := pageTitle(os.Getenv(PageTitleEnv))
	_ = os.Unsetenv(PageEnv)
	_ = os.Unsetenv(PageTitleEnv)
	if !on {
		return out, errOut
	}
	file, ok := out.(*os.File)
	if !ok || !tty.Is(file) {
		return out, errOut
	}
	p := newPager(file, title)
	p.page()
	if ef, ok := errOut.(*os.File); ok && sameFile(file, ef) {
		return p, &pageErr{p: p, file: ef}
	}
	return p, errOut
}

func newPager(file *os.File, title string) *Pager {
	return &Pager{file: file, title: title, col: calmmenu.NewColumn(file, termCols(file))}
}

// pageTitle makes the title printable and fits it in the block.
func pageTitle(s string) string {
	var b strings.Builder
	n := 0
	for i := 0; i < len(s) && n < calmmenu.Width; n++ {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			r = '?'
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "gjallarOS"
	}
	return b.String()
}

// PageWidth is the width of the page column. Programs the installer runs
// on a pseudo-terminal get it as their terminal width (installlog.Run).
func (p *Pager) PageWidth() int { return calmmenu.Width }

func (p *Pager) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.col.Write(b)
}

// Terminal returns the terminal itself. Secret recovery material is
// written there and never passes through the Pager.
func (p *Pager) Terminal() io.Writer { return p.file }

// page starts a new page after an answer, unless a secret is on the
// screen: then only the column starts again.
func (p *Pager) page() {
	p.mu.Lock()
	defer p.mu.Unlock()
	cols := termCols(p.file)
	if p.secret {
		p.secret = false
		p.col = calmmenu.NewColumn(p.file, cols)
		return
	}
	if c, err := calmmenu.Head(p.file, p.title, cols, true); err == nil {
		p.col = c
	}
}

func (p *Pager) showedSecret() {
	p.mu.Lock()
	p.secret = true
	p.mu.Unlock()
}

// pageErr is errOut on the same terminal: the same column, the real file
// behind it.
type pageErr struct {
	p    *Pager
	file *os.File
}

func (e *pageErr) Write(b []byte) (int, error) { return e.p.Write(b) }
func (e *pageErr) Terminal() io.Writer         { return e.file }
func (e *pageErr) PageWidth() int              { return e.p.PageWidth() }

// pagerOf finds the Pager under logging writers around out.
func pagerOf(out io.Writer) *Pager {
	for {
		switch w := out.(type) {
		case *Pager:
			return w
		case *pageErr:
			return w.p
		case terminalWriter:
			out = w.Terminal()
		default:
			return nil
		}
	}
}

// Paged reports whether out draws pages.
func Paged(out io.Writer) bool { return pagerOf(out) != nil }

// UsePages sets u up for page mode when its output draws pages: text
// wraps in the block, and keys typed while no question was up are dropped
// before each question, as the stick menu does. A key pressed during a
// long step must not answer the next question.
func (u *UI) UsePages(in io.Reader) {
	if !Paged(u.Out) {
		return
	}
	u.Wrap = true
	if file, ok := in.(*os.File); ok && tty.Is(file) {
		u.Flush = func(reset bool) {
			_ = unix.IoctlSetInt(int(file.Fd()), unix.TCFLSH, unix.TCIFLUSH)
			if reset {
				u.Reader.Reset(file)
			}
		}
	}
}

func termCols(f *os.File) int {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 80
	}
	return int(ws.Col)
}

func sameFile(a, b *os.File) bool {
	if a == b {
		return true
	}
	var sa, sb unix.Stat_t
	if unix.Fstat(int(a.Fd()), &sa) != nil || unix.Fstat(int(b.Fd()), &sb) != nil {
		return false
	}
	return sa.Dev == sb.Dev && sa.Ino == sb.Ino && sa.Rdev == sb.Rdev
}
