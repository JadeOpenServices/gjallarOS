package devgate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The gate's own PIN budget. The security chip counts wrong PINs from
// every source in one dictionary-attack counter and then refuses PINs at
// startup too, so the gate may use only a small part of it: PINTries
// wrong PINs per PINWindow, after that only the recovery key.
const (
	PINTries  = 3
	PINWindow = 2 * time.Hour
)

// Budget is the helper's record of recent wrong PINs.
type Budget struct {
	PINFails []time.Time `json:"pinFails"`
}

// Left returns the PIN tries left at now, and when one comes back if none
// are left.
func (b Budget) Left(now time.Time) (int, time.Time) {
	recent := b.recent(now)
	if len(recent) < PINTries {
		return PINTries - len(recent), time.Time{}
	}
	return 0, recent[len(recent)-PINTries].Add(PINWindow)
}

// Fail records a wrong PIN at now and forgets the ones outside the window.
func (b *Budget) Fail(now time.Time) {
	b.PINFails = append(b.recent(now), now)
}

// Undo forgets the newest wrong PIN: the helper records one before each
// test, so a crash in the middle still counts the try.
func (b *Budget) Undo() {
	if n := len(b.PINFails); n > 0 {
		b.PINFails = b.PINFails[:n-1]
	}
}

func (b Budget) recent(now time.Time) []time.Time {
	var out []time.Time
	for _, t := range b.PINFails {
		// A time ahead of now (clock moved back) still counts.
		if now.Sub(t) < PINWindow {
			out = append(out, t)
		}
	}
	return out
}

// LoadBudget reads the budget at path. A missing file is a full budget; a
// broken one counts as spent, so damage cannot buy new tries.
func LoadBudget(path string, now time.Time) (Budget, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Budget{}, nil
	}
	spent := Budget{PINFails: make([]time.Time, PINTries)}
	for i := range spent.PINFails {
		spent.PINFails[i] = now
	}
	if err != nil {
		return spent, fmt.Errorf("read the PIN budget: %w", err)
	}
	var b Budget
	if err := json.Unmarshal(raw, &b); err != nil {
		return spent, fmt.Errorf("read the PIN budget: %w", err)
	}
	return b, nil
}

// SaveBudget writes b to path and syncs it before the helper tests a PIN.
func SaveBudget(path string, b Budget) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".budget-*")
	if err != nil {
		return fmt.Errorf("write the PIN budget: %w", err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(raw)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("write the PIN budget: %w", err)
	}
	return nil
}
