// Package circuitbreaker implements the lock-free circuit breaker that guards
// the LinUCB decision path. When a backend produces a run
// of consecutive failures the breaker trips Open, short-circuiting requests to
// the fallback policy; after a cooldown it admits a single trial request
// (HalfOpen) and either recovers (Closed) or re-trips.
//
// All state is held in atomics so the hot path (AllowRequest) takes no locks.
package circuitbreaker

import (
	"sync/atomic"
	"time"
)

// State values for the breaker.
const (
	StateClosed   int32 = 0 // normal operation; requests flow
	StateOpen     int32 = 1 // tripped; requests short-circuit to fallback
	StateHalfOpen int32 = 2 // cooldown elapsed; admit a trial request
)

// Default thresholds.
const (
	DefaultMaxConsecutiveErrors = 5
	DefaultErrorThreshold       = 10
	DefaultCooldown             = 5 * time.Second
)

// CircuitBreaker is a per-target breaker. The zero value is not usable; use New.
type CircuitBreaker struct {
	consecutiveErrors atomic.Int64
	errorScore        atomic.Int64 // leaky bucket error accumulator (JANUS-02)
	state             atomic.Int32
	lastTrip          atomic.Int64 // Unix nanoseconds of the most recent trip
	trialInFlight     atomic.Bool  // strictly admits one in-flight probe during HalfOpen

	maxErrors      int64
	errorThreshold int64
	cooldown       time.Duration
	now            func() time.Time // injectable clock for tests
}

// New returns a Closed breaker with the defaults.
func New() *CircuitBreaker {
	return NewWithThresholds(DefaultMaxConsecutiveErrors, DefaultErrorThreshold, DefaultCooldown)
}

// NewWithConfig returns a Closed breaker with consecutive error thresholds (error threshold disabled).
func NewWithConfig(maxConsecutiveErrors int, cooldown time.Duration) *CircuitBreaker {
	return NewWithThresholds(maxConsecutiveErrors, 0, cooldown)
}

// NewWithThresholds returns a Closed breaker with explicit consecutive and leaky-bucket error thresholds.
func NewWithThresholds(maxConsecutiveErrors int, errorThreshold int, cooldown time.Duration) *CircuitBreaker {
	if maxConsecutiveErrors < 1 {
		maxConsecutiveErrors = DefaultMaxConsecutiveErrors
	}
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	cb := &CircuitBreaker{
		maxErrors:      int64(maxConsecutiveErrors),
		errorThreshold: int64(errorThreshold),
		cooldown:       cooldown,
		now:            time.Now,
	}
	cb.state.Store(StateClosed)
	return cb
}

// RecordResult feeds the outcome of one request back to the breaker. A failure
// increments the consecutive-error counter and leaky bucket score, tripping the
// breaker Open once a threshold is reached or immediately if a HalfOpen trial failed.
// A success resets consecutive errors, decrements leaky bucket score, and closes
// a HalfOpen breaker.
func (cb *CircuitBreaker) RecordResult(failed bool) {
	if failed {
		n := cb.consecutiveErrors.Add(1)
		var score int64
		if cb.errorThreshold > 0 {
			score = cb.errorScore.Add(6)
		}
		isHalfOpen := cb.state.Load() == StateHalfOpen
		if isHalfOpen || n >= cb.maxErrors || (cb.errorThreshold > 0 && score >= cb.errorThreshold) {
			cb.lastTrip.Store(cb.now().UnixNano())
			cb.state.Store(StateOpen)
			if cb.errorThreshold > 0 && score < cb.errorThreshold {
				cb.errorScore.Store(cb.errorThreshold)
			}
		}
		cb.trialInFlight.Store(false)
		return
	}
	cb.consecutiveErrors.Store(0)
	if cb.errorThreshold > 0 {
		for {
			cur := cb.errorScore.Load()
			if cur <= 0 {
				break
			}
			if cb.errorScore.CompareAndSwap(cur, cur-1) {
				break
			}
		}
	}
	// A successful trial request recovers the breaker. CAS so we don't clobber a
	// concurrent re-trip into Open.
	cb.state.CompareAndSwap(StateHalfOpen, StateClosed)
	cb.trialInFlight.Store(false)
}

// AllowRequest reports whether a request may proceed through the LinUCB path.
// When false, the caller routes via the fallback policy instead.
// An Open breaker whose cooldown has elapsed transitions itself to HalfOpen and
// admits strictly a single trial request; concurrent callers receive false until
// the trial outcome lands.
func (cb *CircuitBreaker) AllowRequest() bool {
	switch cb.state.Load() {
	case StateClosed:
		return true
	case StateOpen:
		elapsed := cb.now().Sub(time.Unix(0, cb.lastTrip.Load()))
		if elapsed > cb.cooldown {
			// Transition to HalfOpen and immediately claim the single trial probe.
			if cb.state.CompareAndSwap(StateOpen, StateHalfOpen) {
				return cb.trialInFlight.CompareAndSwap(false, true)
			}
		}
		return false
	case StateHalfOpen:
		// Strictly admit only one in-flight trial. Any concurrent callers while
		// trial is in flight receive false.
		return cb.trialInFlight.CompareAndSwap(false, true)
	default:
		return false
	}
}

// State returns the current breaker state (for metrics / introspection).
func (cb *CircuitBreaker) State() int32 { return cb.state.Load() }

// ConsecutiveErrors returns the current consecutive-failure count.
func (cb *CircuitBreaker) ConsecutiveErrors() int64 { return cb.consecutiveErrors.Load() }

// ErrorScore returns the current leaky-bucket error score.
func (cb *CircuitBreaker) ErrorScore() int64 { return cb.errorScore.Load() }

// Available inspects eligibility without reserving a half-open probe.
func (cb *CircuitBreaker) Available() bool {
	switch cb.state.Load() {
	case StateClosed:
		return true
	case StateOpen:
		return cb.now().Sub(time.Unix(0, cb.lastTrip.Load())) > cb.cooldown
	case StateHalfOpen:
		return !cb.trialInFlight.Load()
	}
	return false
}
