package server

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// limitsConfig is the example config with its rate limits replaced.
func limitsConfig(t *testing.T, rateMax, failMax int) string {
	t.Helper()
	cfg := exampleConfig(t)
	cfg = strings.Replace(cfg, "rate_max: 60 ", "rate_max: "+strconv.Itoa(rateMax)+" ", 1)
	cfg = strings.Replace(cfg, "rate_fail_max: 10 ", "rate_fail_max: "+strconv.Itoa(failMax)+" ", 1)
	if !strings.Contains(cfg, "rate_max: "+strconv.Itoa(rateMax)+" ") || !strings.Contains(cfg, "rate_fail_max: "+strconv.Itoa(failMax)+" ") {
		t.Fatal("the example config's limits have moved")
	}
	return cfg
}

// fakeClock is a clock a test moves by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (f *fixture) clock() *fakeClock {
	c := &fakeClock{t: time.Date(2026, 10, 5, 4, 31, 0, 0, time.UTC)}
	f.srv.budgets.now = c.now
	return c
}

var wrongBearer = strings.Repeat("z", 64)

// TestAcceptanceFailureBudget is the first acceptance criterion of 03:
// with rate_fail_max 10, eleven requests with a wrong bearer end with 429,
// and a request with the right one that follows is a 201.
func TestAcceptanceFailureBudget(t *testing.T) {
	f := newFixture(t, limitsConfig(t, 60, 10))
	f.clock()
	for i := 1; i <= 11; i++ {
		rec := f.do(t, req{path: "/notify/backup", bearer: wrongBearer, body: "x"})
		want := http.StatusUnauthorized
		if i == 11 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("wrong bearer #%d: status %d, want %d", i, rec.Code, want)
		}
		errorBody(t, rec)
	}
	rec := f.do(t, req{path: "/notify/backup", bearer: secret, body: "x"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("right bearer: status %d, body %s; want 201", rec.Code, rec.Body)
	}
}

// TestFailureBudgetCoversRoutingAndMethod: 404, 405 and 401 spend from the
// same budget, and each becomes a 429 once it is exhausted.
func TestFailureBudgetCoversRoutingAndMethod(t *testing.T) {
	f := newFixture(t, limitsConfig(t, 60, 3))
	f.clock()
	for _, c := range []struct {
		r      req
		status int
	}{
		{req{path: "/nope", bearer: secret, body: "x"}, 404},
		{req{method: "GET", path: "/notify/backup", bearer: secret}, 405},
		{req{path: "/notify/backup", body: "x"}, 401},
	} {
		if rec := f.do(t, c.r); rec.Code != c.status {
			t.Fatalf("%s %s: status %d, want %d", c.r.method, c.r.path, rec.Code, c.status)
		}
	}
	for _, r := range []req{
		{path: "/nope", bearer: secret, body: "x"},
		{path: "/x/../notify", bearer: secret, body: "x"},
		{method: "GET", path: "/notify/backup", bearer: secret},
		{path: "/notify/backup", body: "x"},
		{path: "/comfy/a/b", bearer: secret, body: "x"},
	} {
		rec := f.do(t, r)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("%s %s: status %d, want 429", r.method, r.path, rec.Code)
		}
		errorBody(t, rec)
		for _, h := range []string{"Allow", "WWW-Authenticate"} {
			if v := rec.Header().Get(h); v != "" {
				t.Errorf("%s %s: a 429 carries %s: %q", r.method, r.path, h, v)
			}
		}
	}
	if n := len(f.stub.Emits(t)); n != 0 {
		t.Errorf("decree emit ran %d times", n)
	}
}

// TestRequestBudget: authenticated requests spend rate_max, all endpoints
// together, and the one over it is a 429 that runs no decree.
func TestRequestBudget(t *testing.T) {
	f := newFixture(t, limitsConfig(t, 3, 10))
	f.clock()
	for _, r := range []req{
		{path: "/notify", bearer: secret, body: "x"},
		{path: "/notify/backup", bearer: secret, body: "x"},
		{path: "/comfy/a/b", bearer: comfySecret, body: "x"},
	} {
		if rec := f.do(t, r); rec.Code != http.StatusCreated {
			t.Fatalf("%s: status %d, want 201", r.path, rec.Code)
		}
	}
	rec := f.do(t, req{path: "/notify", bearer: secret, body: "x"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over rate_max: status %d, want 429", rec.Code)
	}
	errorBody(t, rec)
	if n := len(f.stub.Emits(t)); n != 3 {
		t.Errorf("decree emit ran %d times, want 3", n)
	}
}

// TestRetryAfterAndWindow: a 429 says when the window ends, and the next
// window has a full budget.
func TestRetryAfterAndWindow(t *testing.T) {
	for _, c := range []struct {
		name string
		r    req
	}{
		{"request budget", req{path: "/notify/backup", bearer: secret, body: "x"}},
		{"failure budget", req{path: "/notify/backup", bearer: wrongBearer, body: "x"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, limitsConfig(t, 1, 1))
			clk := f.clock()
			first := f.do(t, c.r).Code
			clk.add(20*time.Second + 500*time.Millisecond)
			rec := f.do(t, c.r)
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status %d, want 429", rec.Code)
			}
			if got := rec.Header().Get("Retry-After"); got != "40" {
				t.Errorf("Retry-After %q, want 40 (39.5 s rounded up)", got)
			}
			clk.add(39*time.Second + 500*time.Millisecond)
			if rec := f.do(t, c.r); rec.Code != first {
				t.Errorf("next window: status %d, want %d", rec.Code, first)
			}
		})
	}
}

// TestUnauthenticatedCannotStarveCallers: exhausting the failure budget
// leaves rate_max to authenticated callers.
func TestUnauthenticatedCannotStarveCallers(t *testing.T) {
	f := newFixture(t, limitsConfig(t, 5, 2))
	f.clock()
	for i := 0; i < 50; i++ {
		f.do(t, req{path: "/notify/backup", bearer: wrongBearer, body: "x"})
		f.do(t, req{path: "/nope", body: "x"})
	}
	for i := 0; i < 5; i++ {
		if rec := f.do(t, req{path: "/notify/backup", bearer: secret, body: "x"}); rec.Code != http.StatusCreated {
			t.Fatalf("authenticated #%d: status %d, want 201", i+1, rec.Code)
		}
	}
}

// TestAuthenticatedCannotStarveFailures: exhausting rate_max spends nothing
// from the failure budget, so a rejected request is still told why.
func TestAuthenticatedCannotStarveFailures(t *testing.T) {
	f := newFixture(t, limitsConfig(t, 2, 1))
	f.clock()
	for i := 0; i < 10; i++ {
		f.do(t, req{path: "/notify/backup", bearer: secret, body: "x"})
	}
	if rec := f.do(t, req{path: "/notify/backup", bearer: wrongBearer, body: "x"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer: status %d, want 401", rec.Code)
	}
}

// TestAuthenticated400SpendsRequestBudget: a 400 or 413 from an
// authenticated caller spends rate_max, not the failure budget.
func TestAuthenticated400SpendsRequestBudget(t *testing.T) {
	f := newFixture(t, strings.Replace(limitsConfig(t, 4, 1), "max_body_bytes: 262144", "max_body_bytes: 8", 1))
	f.clock()
	for _, c := range []struct {
		r      req
		status int
	}{
		{req{path: "/notify/a$b", bearer: secret, body: "x"}, 400},
		{req{path: "/notify/backup", bearer: secret}, 400},
		{req{path: "/notify/backup", bearer: secret, body: "123456789"}, 413},
	} {
		if rec := f.do(t, c.r); rec.Code != c.status {
			t.Fatalf("%s: status %d, want %d", c.r.path, rec.Code, c.status)
		}
	}
	// The failure budget is untouched: its one request is still a 401.
	if rec := f.do(t, req{path: "/notify/backup", bearer: wrongBearer, body: "x"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer: status %d, want 401", rec.Code)
	}
	// The three 400s spent rate_max: one request is left of four.
	if rec := f.do(t, req{path: "/notify/backup", bearer: secret, body: "x"}); rec.Code != http.StatusCreated {
		t.Fatalf("fourth authenticated: status %d, want 201", rec.Code)
	}
	if rec := f.do(t, req{path: "/notify/backup", bearer: secret, body: "x"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("fifth authenticated: status %d, want 429", rec.Code)
	}
}
