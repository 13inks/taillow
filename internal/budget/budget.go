// Package budget counts LLM tokens per caller per UTC day.
package budget

import (
	"errors"
	"sync"
	"time"
)

// ErrExhausted means the spend would take the identity past its daily limit.
var ErrExhausted = errors.New("daily token budget exhausted")

// ErrInvalid means the identity is empty, or tokens or limit is not positive.
var ErrInvalid = errors.New("invalid spend")

// Ledger counts tokens per identity per UTC calendar day. Safe for concurrent use.
type Ledger struct {
	now func() time.Time

	mu    sync.Mutex
	spent map[string]dayTotal // guarded by mu
}

type dayTotal struct {
	day   time.Time // UTC midnight that starts the day the total belongs to
	total int64
}

// New returns an empty Ledger that reads the current time from now.
// A nil now means time.Now.
func New(now func() time.Time) *Ledger {
	if now == nil {
		now = time.Now
	}
	return &Ledger{now: now, spent: make(map[string]dayTotal)}
}

// Spend records tokens against identity for the current UTC day, if the day's
// total stays within limit.
func (l *Ledger) Spend(identity string, tokens, limit int64) (remaining int64, resetAt time.Time, err error) {
	if identity == "" || tokens <= 0 || limit <= 0 {
		return 0, time.Time{}, ErrInvalid
	}
	day := startOfDay(l.now())
	resetAt = day.AddDate(0, 0, 1)

	l.mu.Lock()
	defer l.mu.Unlock()
	used := l.usedLocked(identity, day)
	// tokens > limit-used, not used+tokens > limit: the sum can overflow.
	if tokens > limit-used {
		return max(0, limit-used), resetAt, ErrExhausted
	}
	used += tokens
	l.spent[identity] = dayTotal{day: day, total: used}
	return limit - used, resetAt, nil
}

// Used returns the tokens identity has spent in the current UTC day.
func (l *Ledger) Used(identity string) int64 {
	day := startOfDay(l.now())
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.usedLocked(identity, day)
}

func (l *Ledger) usedLocked(identity string, day time.Time) int64 {
	t, ok := l.spent[identity]
	if !ok || !t.day.Equal(day) {
		return 0
	}
	return t.total
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
