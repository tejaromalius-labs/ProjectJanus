// Package policy implements the classic load-balancing baselines Janus is
// compared against: round robin, least connections, power of two choices
// and Peak EWMA (Finagle-style latency cost, chosen by two random choices).
package policy

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// Backend is one upstream with its live in-flight count.
type Backend struct {
	ID       string
	Addr     string
	inFlight atomic.Int64
}

func (b *Backend) Acquire()        { b.inFlight.Add(1) }
func (b *Backend) Release()        { b.inFlight.Add(-1) }
func (b *Backend) InFlight() int64 { return b.inFlight.Load() }

// Policy picks a backend and may learn from completed requests.
type Policy interface {
	Pick() *Backend
	Observe(b *Backend, latency time.Duration)
}

// New returns the named policy over a fixed backend set.
func New(name string, backends []*Backend) (Policy, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("no backends")
	}
	switch name {
	case "rr":
		return &roundRobin{backends: backends}, nil
	case "lc":
		return &leastConn{backends: backends}, nil
	case "p2c":
		return &powerOfTwo{backends: backends}, nil
	case "pewma":
		return newPeakEWMA(backends, 10*time.Second, time.Now), nil
	}
	return nil, fmt.Errorf("unknown policy %q (want rr, lc, p2c or pewma)", name)
}

type roundRobin struct {
	backends []*Backend
	next     atomic.Uint64
}

func (p *roundRobin) Pick() *Backend {
	return p.backends[(p.next.Add(1)-1)%uint64(len(p.backends))]
}
func (p *roundRobin) Observe(*Backend, time.Duration) {}

// leastConn scans every backend and picks the fewest in flight; ties are random.
type leastConn struct{ backends []*Backend }

func (p *leastConn) Pick() *Backend {
	var best *Backend
	ties := 0
	for _, b := range p.backends {
		n := b.InFlight()
		switch {
		case best == nil || n < best.InFlight():
			best, ties = b, 1
		case n == best.InFlight():
			ties++
			if rand.IntN(ties) == 0 { // reservoir sampling over ties
				best = b
			}
		}
	}
	return best
}
func (p *leastConn) Observe(*Backend, time.Duration) {}

// twoChoices samples two distinct backends uniformly at random.
func twoChoices(backends []*Backend) (*Backend, *Backend) {
	a := rand.IntN(len(backends))
	if len(backends) == 1 {
		return backends[a], backends[a]
	}
	b := rand.IntN(len(backends) - 1)
	if b >= a {
		b++
	}
	return backends[a], backends[b]
}

func lower(a, b *Backend, ca, cb float64) *Backend {
	if ca < cb || (ca == cb && rand.IntN(2) == 0) {
		return a
	}
	return b
}

type powerOfTwo struct{ backends []*Backend }

func (p *powerOfTwo) Pick() *Backend {
	a, b := twoChoices(p.backends)
	return lower(a, b, float64(a.InFlight()), float64(b.InFlight()))
}
func (p *powerOfTwo) Observe(*Backend, time.Duration) {}

// peakEWMA keeps a per-backend latency estimate that jumps to any higher
// observation (the "peak") and otherwise decays with time constant tau. Cost
// is estimate × (in flight + 1); a backend with no estimate yet but requests
// pending is treated as very expensive so it is not flooded.
type peakEWMA struct {
	backends []*Backend
	tau      time.Duration
	now      func() time.Time
	mu       sync.Mutex
	cost     map[*Backend]*ewma
}

type ewma struct {
	ms    float64
	stamp time.Time
}

func newPeakEWMA(backends []*Backend, tau time.Duration, now func() time.Time) *peakEWMA {
	p := &peakEWMA{backends: backends, tau: tau, now: now, cost: make(map[*Backend]*ewma)}
	for _, b := range backends {
		p.cost[b] = &ewma{}
	}
	return p
}

func (p *peakEWMA) decayed(e *ewma, now time.Time) float64 {
	if e.stamp.IsZero() {
		return 0
	}
	return e.ms * math.Exp(-float64(now.Sub(e.stamp))/float64(p.tau))
}

func (p *peakEWMA) score(b *Backend, now time.Time) float64 {
	ms := p.decayed(p.cost[b], now)
	pending := b.InFlight()
	if ms == 0 && pending > 0 {
		return 1e12 + float64(pending)
	}
	return ms * float64(pending+1)
}

func (p *peakEWMA) Pick() *Backend {
	a, b := twoChoices(p.backends)
	p.mu.Lock()
	now := p.now()
	ca, cb := p.score(a, now), p.score(b, now)
	p.mu.Unlock()
	return lower(a, b, ca, cb)
}

func (p *peakEWMA) Observe(b *Backend, latency time.Duration) {
	ms := float64(latency) / float64(time.Millisecond)
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	e := p.cost[b]
	if e.stamp.IsZero() || ms > e.ms {
		e.ms = ms
	} else {
		w := math.Exp(-float64(now.Sub(e.stamp)) / float64(p.tau))
		e.ms = e.ms*w + ms*(1-w)
	}
	e.stamp = now
}
