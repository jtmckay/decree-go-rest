package server

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jtmckay/decree-api/internal/config"
)

// window is one fixed-window rate budget (SPEC.md §6). It is global: every
// request spends from the same count.
type window struct {
	mu    sync.Mutex
	max   int
	size  time.Duration
	start time.Time
	used  int
}

// take spends one request from the budget at now. When the budget is
// exhausted it spends nothing and returns how long until the window ends.
func (w *window) take(now time.Time) (ok bool, retryAfter time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.start.IsZero() || !now.Before(w.start.Add(w.size)) {
		w.start, w.used = now, 0
	}
	if w.used >= w.max {
		return false, w.start.Add(w.size).Sub(now)
	}
	w.used++
	return true, 0
}

func (w *window) set(max int, size time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.max, w.size = max, size
}

// budgets are the two budgets of SPEC.md §6. A reload keeps them, so a
// config change does not refill them; it only changes their limits.
type budgets struct {
	// all is rate_max: spent only by authenticated requests.
	all window
	// fail is rate_fail_max: spent only by requests that fail routing, the
	// method check or authentication.
	fail window
	// now is the clock; time.Now outside tests.
	now func() time.Time
}

func newBudgets(l config.Limits) *budgets {
	b := &budgets{now: time.Now}
	b.setLimits(l)
	return b
}

func (b *budgets) setLimits(l config.Limits) {
	b.all.set(l.RateMax, l.RateWindow.Std())
	b.fail.set(l.RateFailMax, l.RateWindow.Std())
}

// tooManyRequests is the 429 of an exhausted budget, with Retry-After in
// whole seconds, rounded up.
func tooManyRequests(w http.ResponseWriter, retryAfter time.Duration) {
	secs := int64((retryAfter + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
	writeError(w, http.StatusTooManyRequests, "too many requests")
}
