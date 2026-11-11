package policy

import (
	"testing"
	"time"
)

func backends(n int) []*Backend {
	out := make([]*Backend, n)
	for i := range out {
		out[i] = &Backend{ID: string(rune('a' + i))}
	}
	return out
}

func TestUnknownPolicy(t *testing.T) {
	if _, err := New("ewma", backends(2)); err == nil {
		t.Fatal("expected error")
	}
}

func TestRoundRobinCyclesEvenly(t *testing.T) {
	bs := backends(4)
	p, _ := New("rr", bs)
	for i := 0; i < 400; i++ {
		if got := p.Pick(); got != bs[i%4] {
			t.Fatalf("pick %d = %s, want %s", i, got.ID, bs[i%4].ID)
		}
	}
}

func TestLeastConnPicksMinimumAndBreaksTiesRandomly(t *testing.T) {
	bs := backends(4)
	bs[0].inFlight.Store(3)
	bs[1].inFlight.Store(1)
	bs[2].inFlight.Store(1)
	bs[3].inFlight.Store(2)
	p, _ := New("lc", bs)
	seen := map[*Backend]int{}
	for i := 0; i < 2000; i++ {
		seen[p.Pick()]++
	}
	if seen[bs[0]] != 0 || seen[bs[3]] != 0 {
		t.Fatalf("picked a non-minimum backend: %v", seen)
	}
	if seen[bs[1]] < 800 || seen[bs[2]] < 800 {
		t.Fatalf("ties not split randomly: %v", seen)
	}
}

func TestTwoChoicesAreDistinct(t *testing.T) {
	bs := backends(3)
	for i := 0; i < 1000; i++ {
		if a, b := twoChoices(bs); a == b {
			t.Fatal("sampled the same backend twice")
		}
	}
}

func TestP2CPrefersFewerInFlight(t *testing.T) {
	bs := backends(2)
	bs[0].inFlight.Store(5)
	p, _ := New("p2c", bs)
	for i := 0; i < 100; i++ {
		if p.Pick() != bs[1] {
			t.Fatal("p2c picked the busier backend")
		}
	}
}

func TestPeakEWMAPrefersLowerCostAndDecays(t *testing.T) {
	bs := backends(2)
	now := time.Unix(0, 0)
	p := newPeakEWMA(bs, 10*time.Second, func() time.Time { return now })
	p.Observe(bs[0], 200*time.Millisecond)
	p.Observe(bs[1], 5*time.Millisecond)
	for i := 0; i < 100; i++ {
		if p.Pick() != bs[1] {
			t.Fatal("pewma picked the slow backend")
		}
	}
	// Peak: a higher observation replaces the estimate immediately.
	p.Observe(bs[1], 500*time.Millisecond)
	if got := p.cost[bs[1]].ms; got != 500 {
		t.Fatalf("peak not taken: %v", got)
	}
	// Decay: one time constant later the estimate is about 1/e.
	now = now.Add(10 * time.Second)
	if got := p.decayed(p.cost[bs[1]], now); got < 183 || got > 185 {
		t.Fatalf("decayed cost = %v, want ~184", got)
	}
}

func TestPeakEWMAAvoidsUnmeasuredBusyBackend(t *testing.T) {
	bs := backends(2)
	p := newPeakEWMA(bs, 10*time.Second, time.Now)
	p.Observe(bs[1], 50*time.Millisecond)
	bs[0].inFlight.Store(1)
	for i := 0; i < 100; i++ {
		if p.Pick() != bs[1] {
			t.Fatal("unmeasured busy backend was preferred")
		}
	}
}
