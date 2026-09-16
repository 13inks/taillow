package budget

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

// clock is a settable time source that is safe to read from many goroutines.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.t = t }

var (
	noon     = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	midnight = time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
)

func TestLedger(t *testing.T) {
	type spend struct {
		id            string
		tokens, limit int64
	}
	check := func(t *testing.T, l *Ledger, s spend, wantRem int64, wantReset time.Time, wantErr error) {
		t.Helper()
		rem, reset, err := l.Spend(s.id, s.tokens, s.limit)
		if wantErr == nil && err != nil {
			t.Fatalf("Spend(%v) err = %v, want nil", s, err)
		}
		if wantErr != nil && !errors.Is(err, wantErr) {
			t.Fatalf("Spend(%v) err = %v, want %v", s, err, wantErr)
		}
		if rem != wantRem {
			t.Errorf("Spend(%v) remaining = %d, want %d", s, rem, wantRem)
		}
		if !reset.Equal(wantReset) {
			t.Errorf("Spend(%v) resetAt = %v, want %v", s, reset, wantReset)
		}
	}
	atNoon := func() *Ledger { c := &clock{t: noon}; return New(c.now) }

	t.Run("first spend returns what is left and the next midnight", func(t *testing.T) {
		check(t, atNoon(), spend{"a", 30, 100}, 70, midnight, nil)
	})
	t.Run("spending exactly to the limit succeeds with nothing left", func(t *testing.T) {
		check(t, atNoon(), spend{"a", 100, 100}, 0, midnight, nil)
	})
	t.Run("over the limit is exhausted and records nothing", func(t *testing.T) {
		l := atNoon()
		check(t, l, spend{"a", 60, 100}, 40, midnight, nil)
		check(t, l, spend{"a", 41, 100}, 40, midnight, ErrExhausted)
		if got := l.Used("a"); got != 60 {
			t.Errorf("Used = %d, want 60", got)
		}
	})
	t.Run("a smaller spend still fits after an exhausted one", func(t *testing.T) {
		l := atNoon()
		check(t, l, spend{"a", 60, 100}, 40, midnight, nil)
		check(t, l, spend{"a", 50, 100}, 40, midnight, ErrExhausted)
		check(t, l, spend{"a", 40, 100}, 0, midnight, nil)
	})
	t.Run("identities are independent", func(t *testing.T) {
		l := atNoon()
		check(t, l, spend{"a", 100, 100}, 0, midnight, nil)
		check(t, l, spend{"b", 10, 100}, 90, midnight, nil)
	})
	for _, s := range []spend{{"", 1, 10}, {"a", 0, 10}, {"a", -1, 10}, {"a", 1, 0}} {
		t.Run("invalid spend is refused with zero values", func(t *testing.T) {
			l := atNoon()
			check(t, l, s, 0, time.Time{}, ErrInvalid)
			if got := l.Used(s.id); got != 0 {
				t.Errorf("Used = %d after an invalid spend, want 0", got)
			}
		})
	}
	t.Run("the total resets at UTC midnight", func(t *testing.T) {
		c := &clock{t: midnight.Add(-time.Second)}
		l := New(c.now)
		check(t, l, spend{"a", 100, 100}, 0, midnight, nil)
		c.set(midnight)
		if got := l.Used("a"); got != 0 {
			t.Errorf("Used after midnight = %d, want 0", got)
		}
		check(t, l, spend{"a", 10, 100}, 90, midnight.AddDate(0, 0, 1), nil)
	})
	t.Run("exactly midnight resets at the next midnight", func(t *testing.T) {
		c := &clock{t: midnight}
		check(t, New(c.now), spend{"a", 1, 10}, 9, midnight.AddDate(0, 0, 1), nil)
	})
	t.Run("the day is the UTC date, not the local one", func(t *testing.T) {
		// 20:00 on the 16th in UTC-8 is 04:00 on the 17th in UTC.
		pacific := time.FixedZone("UTC-8", -8*60*60)
		c := &clock{t: time.Date(2026, 9, 16, 20, 0, 0, 0, pacific)}
		_, reset, err := New(c.now).Spend("a", 1, 10)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		want := midnight.AddDate(0, 0, 1)
		if !reset.Equal(want) || reset.Location() != time.UTC {
			t.Errorf("resetAt = %v (%v), want %v in UTC", reset, reset.Location(), want)
		}
	})
	t.Run("a huge spend is exhausted, not wrapped around", func(t *testing.T) {
		l := atNoon()
		check(t, l, spend{"a", 10, math.MaxInt64}, math.MaxInt64-10, midnight, nil)
		check(t, l, spend{"a", math.MaxInt64, math.MaxInt64}, math.MaxInt64-10, midnight, ErrExhausted)
	})
	t.Run("a later, smaller limit below what was used leaves zero", func(t *testing.T) {
		l := atNoon()
		check(t, l, spend{"a", 80, 100}, 20, midnight, nil)
		check(t, l, spend{"a", 1, 50}, 0, midnight, ErrExhausted)
	})
	t.Run("an unknown identity has used nothing", func(t *testing.T) {
		if got := atNoon().Used("nobody"); got != 0 {
			t.Errorf("Used = %d, want 0", got)
		}
	})
	t.Run("concurrent spends never overshoot the limit", func(t *testing.T) {
		l := atNoon()
		var wg sync.WaitGroup
		var mu sync.Mutex
		ok, exhausted := 0, 0
		for range 100 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, err := l.Spend("a", 1, 50)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					ok++
				case errors.Is(err, ErrExhausted):
					exhausted++
				}
			}()
		}
		wg.Wait()
		if ok != 50 || exhausted != 50 || l.Used("a") != 50 {
			t.Errorf("ok=%d exhausted=%d used=%d, want 50/50/50", ok, exhausted, l.Used("a"))
		}
	})
	t.Run("a nil clock uses the real time", func(t *testing.T) {
		before := time.Now()
		_, reset, err := New(nil).Spend("a", 1, 10)
		if err != nil || !reset.After(before) || reset.Sub(before) > 24*time.Hour {
			t.Errorf("resetAt = %v, err = %v; want within a day after %v", reset, err, before)
		}
	})
	t.Run("returned errors match with errors.Is", func(t *testing.T) {
		l := atNoon()
		_, _, err := l.Spend("", 1, 1)
		if !errors.Is(err, ErrInvalid) || errors.Is(err, ErrExhausted) {
			t.Errorf("invalid spend err = %v", err)
		}
		l.Spend("a", 1, 1)
		_, _, err = l.Spend("a", 1, 1)
		if !errors.Is(err, ErrExhausted) || errors.Is(err, ErrInvalid) {
			t.Errorf("exhausted spend err = %v", err)
		}
	})
}
