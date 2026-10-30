package waitroom

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

type tClock struct {
	mu sync.Mutex
	t  time.Time
}

func tNewClock() *tClock { return &tClock{t: time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)} }
func (c *tClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *tClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func tRoom(t *testing.T, clk *tClock, rate float64, burst, maxQueue int) *Room {
	t.Helper()
	r, err := New(Config{Rate: rate, Burst: burst, PassTTL: 10 * time.Minute, MaxQueue: maxQueue, Secret: []byte("flash-sale-secret"), Now: clk.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func tJoin(t *testing.T, r *Room, user string) Ticket {
	t.Helper()
	tk, err := r.Join(user)
	if err != nil {
		t.Fatalf("Join(%s): %v", user, err)
	}
	return tk
}

func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock in the implementation?)", what, d)
	}
}

// ---------- configuration ----------

func TestNew_ValidatesConfig(t *testing.T) {
	clk := tNewClock()
	bad := []Config{
		{Rate: 0, PassTTL: time.Minute, Secret: []byte("s"), Now: clk.Now},
		{Rate: -1, PassTTL: time.Minute, Secret: []byte("s"), Now: clk.Now},
		{Rate: 10, PassTTL: 0, Secret: []byte("s"), Now: clk.Now},
		{Rate: 10, PassTTL: time.Minute, Secret: nil, Now: clk.Now},
	}
	for i, cfg := range bad {
		if _, err := New(cfg); !errors.Is(err, ErrInvalid) {
			t.Errorf("config %d: err = %v, want ErrInvalid", i, err)
		}
	}
	r, err := New(Config{Rate: 2.5, PassTTL: time.Minute, Secret: []byte("s"), Now: clk.Now})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if _, err := r.Join(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Join(\"\"): %v, want ErrInvalid", err)
	}
}

// ---------- the queue ----------

func TestJoin_PositionsAreFIFOAndStatusCountsDown(t *testing.T) {
	clk := tNewClock()
	r := tRoom(t, clk, 100, 100, 0)
	for i := 1; i <= 5; i++ {
		tk := tJoin(t, r, fmt.Sprintf("u%d", i))
		if tk.Position != i || tk.Pass != "" {
			t.Fatalf("Join(u%d) = %+v, want Position %d and no pass yet", i, tk, i)
		}
		if tk.EstimatedWait != time.Duration(i)*10*time.Millisecond {
			t.Fatalf("u%d EstimatedWait = %v, want %v (position / rate)", i, tk.EstimatedWait, time.Duration(i)*10*time.Millisecond)
		}
	}
	if r.Len() != 5 {
		t.Fatalf("Len = %d, want 5", r.Len())
	}
	if err := r.Leave("u2"); err != nil {
		t.Fatalf("Leave(u2): %v", err)
	}
	st, err := r.Status("u5")
	if err != nil || st.Position != 4 {
		t.Fatalf("Status(u5) after u2 left = %+v, %v; want Position 4", st, err)
	}
	if _, err := r.Status("u2"); !errors.Is(err, ErrNotInQueue) {
		t.Fatalf("Status(u2) after leaving: %v, want ErrNotInQueue", err)
	}
	if err := r.Leave("u2"); !errors.Is(err, ErrNotInQueue) {
		t.Fatalf("Leave(u2) twice: %v, want ErrNotInQueue", err)
	}
	if _, err := r.Status("ghost"); !errors.Is(err, ErrNotInQueue) {
		t.Fatalf("Status(unknown): %v, want ErrNotInQueue", err)
	}
}

func TestJoin_IsIdempotentWhileWaiting(t *testing.T) {
	clk := tNewClock()
	r := tRoom(t, clk, 100, 100, 0)
	tJoin(t, r, "a")
	first := tJoin(t, r, "b")
	tJoin(t, r, "c")
	again := tJoin(t, r, "b")
	if again.Position != first.Position || again.Position != 2 {
		t.Fatalf("second Join(b) = %+v, want the same ticket at position 2 (a double tap must not move the user or take a second slot)", again)
	}
	if r.Len() != 3 {
		t.Fatalf("Len = %d after a duplicate Join, want 3", r.Len())
	}
}

func TestJoin_RefusesWhenQueueIsFull(t *testing.T) {
	clk := tNewClock()
	r := tRoom(t, clk, 1, 1, 3)
	for _, u := range []string{"a", "b", "c"} {
		tJoin(t, r, u)
	}
	if _, err := r.Join("d"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Join into a full queue: %v, want ErrQueueFull (bound the queue, or memory grows with the crowd)", err)
	}
	if tk := tJoin(t, r, "b"); tk.Position != 2 {
		t.Fatalf("a waiting user must still get their ticket back when the queue is full, got %+v", tk)
	}
	if err := r.Leave("a"); err != nil {
		t.Fatal(err)
	}
	if tk := tJoin(t, r, "d"); tk.Position != 3 {
		t.Fatalf("Join after a slot freed = %+v, want position 3", tk)
	}
}

// ---------- admission ----------

func TestTick_AdmitsAtRateInFIFOOrder(t *testing.T) {
	clk := tNewClock()
	r := tRoom(t, clk, 100, 100, 0) // 100 users/s
	for i := 0; i < 250; i++ {
		tJoin(t, r, fmt.Sprintf("u%03d", i))
	}
	if got := r.Tick(); len(got) != 0 {
		t.Fatalf("Tick with no elapsed time admitted %d users, want 0 (no tokens yet)", len(got))
	}
	clk.Advance(time.Second)
	passes := r.Tick()
	if len(passes) != 100 {
		t.Fatalf("Tick after 1 s at 100/s admitted %d, want 100", len(passes))
	}
	for i, p := range passes {
		user, err := r.Verify(p)
		if err != nil || user != fmt.Sprintf("u%03d", i) {
			t.Fatalf("pass %d belongs to %q (%v), want u%03d: admission must be FIFO", i, user, err, i)
		}
	}
	if r.Len() != 150 || r.Admitted() != 100 {
		t.Fatalf("Len=%d Admitted=%d, want 150 waiting and 100 admitted", r.Len(), r.Admitted())
	}
	st, _ := r.Status("u100")
	if st.Position != 1 {
		t.Fatalf("u100 position = %d after 100 admissions, want 1", st.Position)
	}
	st, _ = r.Status("u007")
	if st.Position != 0 || st.Pass == "" || !st.PassExpiresAt.Equal(clk.Now().Add(10*time.Minute)) {
		t.Fatalf("Status(u007) after admission = %+v, want Position 0, a pass and PassExpiresAt = now + PassTTL", st)
	}
	clk.Advance(500 * time.Millisecond)
	if got := r.Tick(); len(got) != 50 {
		t.Fatalf("Tick after 0.5 s admitted %d, want 50 (tokens accrue fractionally)", len(got))
	}
	clk.Advance(5 * time.Second) // 500 tokens accrued, capped at Burst 100
	if got := r.Tick(); len(got) != 100 {
		t.Fatalf("Tick after 5 s idle admitted %d, want 100: unspent tokens are capped at Burst", len(got))
	}
	if r.Len() != 0 {
		t.Fatalf("Len = %d, want 0", r.Len())
	}
	if got := r.Tick(); len(got) != 0 {
		t.Fatalf("Tick on an empty queue admitted %d", len(got))
	}
}

func TestTick_TokensCarryOverUpToBurstWhenNobodyWaits(t *testing.T) {
	clk := tNewClock()
	r := tRoom(t, clk, 10, 30, 0)
	clk.Advance(2 * time.Second) // 20 tokens accrue with nobody waiting
	if got := r.Tick(); len(got) != 0 {
		t.Fatalf("empty Tick admitted %d", len(got))
	}
	for i := 0; i < 80; i++ {
		tJoin(t, r, fmt.Sprintf("u%d", i))
	}
	if got := r.Tick(); len(got) != 20 {
		t.Fatalf("Tick right after joins admitted %d, want 20 (tokens banked while idle, up to Burst)", len(got))
	}
	clk.Advance(10 * time.Second) // 100 would accrue; capped at 30
	if got := r.Tick(); len(got) != 30 {
		t.Fatalf("Tick after 10 s idle admitted %d, want 30: unspent tokens are capped at Burst (an uncapped bucket lets the whole crowd in at once)", len(got))
	}
	if r.Len() != 30 {
		t.Fatalf("Len = %d, want 30 still waiting", r.Len())
	}
}

func TestTick_AdmittedUserJoiningAgainGetsPassNotNewSlot(t *testing.T) {
	clk := tNewClock()
	r := tRoom(t, clk, 10, 10, 0)
	tJoin(t, r, "a")
	clk.Advance(time.Second)
	passes := r.Tick()
	if len(passes) != 1 {
		t.Fatalf("admitted %d, want 1", len(passes))
	}
	tk := tJoin(t, r, "a")
	if tk.Position != 0 || tk.Pass != passes[0] {
		t.Fatalf("Join by an admitted user = %+v, want their existing pass with Position 0", tk)
	}
	if r.Len() != 0 {
		t.Fatalf("Len = %d, an admitted user must not re-enter the queue while their pass is valid", r.Len())
	}
	if err := r.Leave("a"); !errors.Is(err, ErrNotInQueue) {
		t.Fatalf("Leave after admission: %v, want ErrNotInQueue", err)
	}
}

// ---------- passes ----------

func TestVerify_RejectsTamperedForeignAndExpiredPasses(t *testing.T) {
	clk := tNewClock()
	r := tRoom(t, clk, 10, 10, 0)
	tJoin(t, r, "alice")
	clk.Advance(time.Second)
	pass := r.Tick()[0]
	if user, err := r.Verify(pass); err != nil || user != "alice" {
		t.Fatalf("Verify(genuine) = %q, %v", user, err)
	}
	parts := strings.Split(pass, "|")
	if len(parts) != 3 {
		t.Fatalf("pass %q is not userID|expiry|mac", pass)
	}
	bad := map[string]string{
		"other user, same mac":  "mallory|" + parts[1] + "|" + parts[2],
		"later expiry":          parts[0] + "|" + fmt.Sprint(clk.Now().Add(time.Hour).Unix()) + "|" + parts[2],
		"flipped mac byte":      parts[0] + "|" + parts[1] + "|" + flipHex(parts[2]),
		"garbage":               "not-a-pass",
		"empty":                 "",
		"too many fields":       pass + "|extra",
		"non-numeric expiry":    parts[0] + "|soon|" + parts[2],
		"mac of the wrong size": parts[0] + "|" + parts[1] + "|abcd",
	}
	for name, p := range bad {
		if _, err := r.Verify(p); !errors.Is(err, ErrBadPass) {
			t.Errorf("%s: Verify = %v, want ErrBadPass", name, err)
		}
	}
	other, _ := New(Config{Rate: 10, PassTTL: time.Minute, Secret: []byte("another-secret"), Now: clk.Now})
	if _, err := other.Verify(pass); !errors.Is(err, ErrBadPass) {
		t.Fatalf("a pass signed with another secret verified: %v", err)
	}
	clk.Advance(10*time.Minute - time.Second)
	if _, err := r.Verify(pass); err != nil {
		t.Fatalf("Verify just before expiry: %v", err)
	}
	clk.Advance(2 * time.Second)
	if _, err := r.Verify(pass); !errors.Is(err, ErrPassExpired) {
		t.Fatalf("Verify after expiry: %v, want ErrPassExpired", err)
	}
}

func flipHex(s string) string {
	b := []byte(s)
	if b[0] == '0' {
		b[0] = '1'
	} else {
		b[0] = '0'
	}
	return string(b)
}

func TestExpiry_ExpiredUserIsForgottenAndRejoinsAtTheBack(t *testing.T) {
	clk := tNewClock()
	r := tRoom(t, clk, 10, 10, 0)
	tJoin(t, r, "a")
	clk.Advance(time.Second)
	r.Tick()
	if r.Admitted() != 1 {
		t.Fatalf("Admitted = %d, want 1", r.Admitted())
	}
	tJoin(t, r, "b")
	tJoin(t, r, "c")
	clk.Advance(11 * time.Minute) // a's pass expired
	if r.Admitted() != 0 {
		t.Fatalf("Admitted = %d after the pass expired, want 0", r.Admitted())
	}
	if _, err := r.Status("a"); !errors.Is(err, ErrNotInQueue) {
		t.Fatalf("Status(a) after expiry: %v, want ErrNotInQueue (an expired user is forgotten)", err)
	}
	tk := tJoin(t, r, "a")
	if tk.Position != 3 || tk.Pass != "" {
		t.Fatalf("rejoin after expiry = %+v, want a fresh ticket at the back (position 3)", tk)
	}
}

// ---------- concurrency ----------

func TestConcurrent_EveryUserAdmittedExactlyOnce(t *testing.T) {
	clk := tNewClock()
	r := tRoom(t, clk, 200, 200, 0)
	const users = 1000
	var wg sync.WaitGroup
	for i := 0; i < users; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			user := fmt.Sprintf("u%04d", i)
			if _, err := r.Join(user); err != nil {
				t.Errorf("Join(%s): %v", user, err)
			}
			if _, err := r.Status(user); err != nil {
				t.Errorf("Status(%s): %v", user, err)
			}
			if _, err := r.Join(user); err != nil { // double tap
				t.Errorf("second Join(%s): %v", user, err)
			}
		}(i)
	}
	tWait(t, &wg, 10*time.Second, "1000 concurrent joins")
	if r.Len() != users {
		t.Fatalf("Len = %d after %d concurrent joins with double taps, want %d", r.Len(), users, users)
	}
	seen := map[string]int{}
	var mu sync.Mutex
	for round := 0; round < 5; round++ {
		clk.Advance(time.Second)
		var tw sync.WaitGroup
		for g := 0; g < 4; g++ { // several tickers racing: still at most 200 per second in total
			tw.Add(1)
			go func() {
				defer tw.Done()
				for _, p := range r.Tick() {
					user, err := r.Verify(p)
					if err != nil {
						t.Errorf("Verify: %v", err)
					}
					mu.Lock()
					seen[user]++
					mu.Unlock()
				}
			}()
		}
		tWait(t, &tw, 10*time.Second, "concurrent ticks")
		if n := len(seen); n != (round+1)*200 {
			t.Fatalf("after round %d, %d users admitted, want %d (rate 200/s must hold across racing tickers)", round, n, (round+1)*200)
		}
	}
	for user, n := range seen {
		if n != 1 {
			t.Fatalf("%s was admitted %d times", user, n)
		}
	}
	if r.Len() != 0 || r.Admitted() != users {
		t.Fatalf("Len=%d Admitted=%d, want 0 and %d", r.Len(), r.Admitted(), users)
	}
}

func TestConcurrent_JoinsAndLeavesKeepTheQueueConsistent(t *testing.T) {
	clk := tNewClock()
	r := tRoom(t, clk, 50, 50, 300)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				user := fmt.Sprintf("g%d-u%d", g, i%40)
				_, err := r.Join(user)
				if err != nil && !errors.Is(err, ErrQueueFull) {
					t.Errorf("Join: %v", err)
				}
				if i%3 == 0 {
					_ = r.Leave(user)
				}
				_, _ = r.Status(user)
				_ = r.Len()
			}
		}(g)
	}
	tWait(t, &wg, 10*time.Second, "mixed concurrent operations")
	if n := r.Len(); n < 0 || n > 300 {
		t.Fatalf("Len = %d, must stay within the MaxQueue bound of 300", n)
	}
	// Every waiting user must have a consistent, unique position.
	positions := map[int]bool{}
	for g := 0; g < 8; g++ {
		for i := 0; i < 40; i++ {
			st, err := r.Status(fmt.Sprintf("g%d-u%d", g, i))
			if err != nil {
				continue
			}
			if st.Position < 1 || st.Position > r.Len() || positions[st.Position] {
				t.Fatalf("user g%d-u%d has position %d (len %d, duplicate=%v)", g, i, st.Position, r.Len(), positions[st.Position])
			}
			positions[st.Position] = true
		}
	}
	if len(positions) != r.Len() {
		t.Fatalf("%d distinct positions for %d waiting users", len(positions), r.Len())
	}
}
