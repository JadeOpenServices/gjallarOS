package safefs

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// MeasureOptions says what Measure counts.
type MeasureOptions struct {
	// SourceUID owns what is counted, as in CopyOptions: entries of other
	// owners are not counted and not entered. It must be above zero unless
	// AnyOwner is set.
	SourceUID int
	// AnyOwner counts entries of every owner. For a tree root owns, such as
	// the system checkout, which the move copies as a whole.
	AnyOwner bool
	// Skip names paths below src, relative and without a leading or
	// trailing slash, that are not counted and not entered.
	Skip map[string]bool
	// MaxEntries caps how many entries the walk looks at, so a tree of
	// millions of empty files cannot keep root busy or fill its memory with
	// names. It must be above zero. Over it, Measure stops with ErrTooMany.
	MaxEntries int
	// MaxFolder, when above zero, caps the entries of one folder, so one
	// crafted folder cannot fill root's memory with names on its own.
	MaxFolder int
}

// Size is what Measure counted.
type Size struct {
	Files, Folders, Links int
	// Bytes is the apparent size of the files: holes count, so it is never
	// less than what Copy writes.
	Bytes int64
	// Used is what a copy of the same tree counts against its budget at
	// most: Bytes plus EntryCost per file, folder and link.
	Used int64
	// Skipped counts pipes, sockets and devices, Foreign entries of other
	// owners, Left the paths left out through Skip.
	Skipped, Foreign, Left int
}

// Measure adds up the size of everything below src the way Copy counts it,
// without opening any file. Links are counted and never followed; folders
// are opened below their parent's handle, so a link or a mount point
// planted on the way is refused, not entered.
func Measure(src *Dir, opt MeasureOptions) (Size, error) {
	var size Size
	if opt.MaxEntries <= 0 {
		return size, fmt.Errorf("measure %s: no entry cap", src.name)
	}
	if !opt.AnyOwner && opt.SourceUID <= 0 {
		return size, fmt.Errorf("measure %s: no source owner", src.name)
	}
	var top unix.Stat_t
	if err := unix.Fstat(src.fd, &top); err != nil {
		return size, fmt.Errorf("measure %s: %w", src.name, err)
	}
	if !opt.AnyOwner && int(top.Uid) != opt.SourceUID {
		return size, fmt.Errorf("measure %s: it belongs to user %d, not %d", src.name, top.Uid, opt.SourceUID)
	}
	m := &measurer{opt: opt, size: &size}
	err := m.tree(src.fd, src.name, "", 0)
	return size, err
}

type measurer struct {
	opt     MeasureOptions
	size    *Size
	entries int
	// names is what the name lists of the open folders cost, as in copier.
	names int64
}

func (m *measurer) owned(uid uint32) bool {
	return m.opt.AnyOwner || int(uid) == m.opt.SourceUID
}

func (m *measurer) tree(fd int, name, rel string, depth int) error {
	if depth >= MaxDepth {
		return fmt.Errorf("measure %s: %w", name, ErrTooDeep)
	}
	limit := m.opt.MaxEntries - m.entries
	if m.opt.MaxFolder > 0 && m.opt.MaxFolder < limit {
		limit = m.opt.MaxFolder
	}
	names, cost, err := readNames(fd, limit, MaxNameBytes-m.names)
	if err != nil {
		return fmt.Errorf("list %s: %w", name, err)
	}
	m.names += cost
	defer func() { m.names -= cost }()
	m.entries += len(names)
	for _, n := range names {
		r := n
		if rel != "" {
			r = rel + "/" + n
		}
		if m.opt.Skip[r] {
			m.size.Left++
			continue
		}
		if err := m.entry(fd, n, name+"/"+n, r, depth); err != nil {
			return err
		}
	}
	return nil
}

func (m *measurer) entry(dir int, n, path, rel string, depth int) error {
	var st unix.Stat_t
	if err := unix.Fstatat(dir, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if !m.owned(st.Uid) {
		m.size.Foreign++
		return nil
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		m.size.Folders++
		m.size.Used += EntryCost
		s, err := openat2(dir, n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, refusal(err))
		}
		defer unix.Close(s)
		var st2 unix.Stat_t
		if err := unix.Fstat(s, &st2); err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if st2.Ino != st.Ino || st2.Dev != st.Dev || !m.owned(st2.Uid) {
			return fmt.Errorf("open %s: %w", path, ErrChanged)
		}
		return m.tree(s, path, rel, depth+1)
	case unix.S_IFREG:
		m.size.Files++
		m.size.Bytes += st.Size
		m.size.Used += EntryCost + st.Size
	case unix.S_IFLNK:
		m.size.Links++
		m.size.Used += EntryCost
	default:
		m.size.Skipped++
	}
	return nil
}
