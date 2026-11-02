package winicon

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/JadeOpenServices/gjallarOS/internal/winpe"
)

// Resource types (winuser.h).
const (
	rtIcon      = 3
	rtGroupIcon = 14
)

// ProgramIcon lists the icons of the first icon group in a program's
// resources, the one file managers show for it. Named groups come before
// numbered ones, in the order the file lists them.
func ProgramIcon(r io.ReaderAt, size int64) ([]Entry, error) {
	f, err := winpe.Open(r, size)
	if err != nil {
		return nil, noIcon(err)
	}
	group, err := f.Resource(rtGroupIcon, -1, MaxEntryBytes)
	if err != nil {
		return nil, noIcon(err)
	}
	if len(group) < 6 || binary.LittleEndian.Uint16(group[0:]) != 0 || binary.LittleEndian.Uint16(group[2:]) != 1 {
		return nil, ErrNoIcon
	}
	count := int(binary.LittleEndian.Uint16(group[4:]))
	if count > MaxEntries {
		return nil, fmt.Errorf("icon group lists %d icons, more than %d", count, MaxEntries)
	}
	if len(group) < 6+14*count {
		return nil, errors.New("icon group is cut short")
	}
	var entries []Entry
	for i := range count {
		e := group[6+14*i:]
		data, err := f.Resource(rtIcon, int(binary.LittleEndian.Uint16(e[12:])), MaxEntryBytes)
		if err != nil {
			continue
		}
		entries = append(entries, Entry{
			Width:    side(e[0]),
			Height:   side(e[1]),
			BitCount: int(binary.LittleEndian.Uint16(e[6:])),
			Data:     data,
		})
	}
	if len(entries) == 0 {
		return nil, ErrNoIcon
	}
	return entries, nil
}

// noIcon gives ErrNoIcon for a file that is not a program or has no
// readable icon group, and passes other errors on.
func noIcon(err error) error {
	if errors.Is(err, winpe.ErrNotPE) || errors.Is(err, winpe.ErrNotFound) {
		return ErrNoIcon
	}
	return err
}
