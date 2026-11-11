package circuitbreaker

import (
	"sync"
	"testing"
	"time"
)

func TestTripsAfterConsecutiveErrors(t *testing.T) {
	cb := NewWithConfig(5, 30*time.Second)

	for i := 0; i < 4; i++ {
		cb.RecordResult(true)
		if cb.State() != StateClosed {
			t.Fatalf("after %d errors: state = %d, want Closed", i+1, cb.State())
		}
		if !cb.AllowRequest() {
			t.Fatalf("after %d errors: AllowRequest = false, want true", i+1)
		}
	}

	cb.RecordResult(true) // 5th consecutive error trips it
	if cb.State() != StateOpen {
		t.Fatalf("state = %d, want Open", cb.State())
	}
	if cb.AllowRequest() {
		t.Fatal("AllowRequest = true while Open within cooldown, want false")
	}
}

func TestSuccessResetsCounter(t *testing.T) {
	cb := NewWithConfig(5, 30*time.Second)
	for i := 0; i < 4; i++ {
		cb.RecordResult(true)
	}
	cb.RecordResult(false) // success resets streak
	if cb.ConsecutiveErrors() != 0 {
		t.Fatalf("consecutiveErrors = %d, want 0", cb.ConsecutiveErrors())
	}
	// Four more errors should not trip (streak was reset).
	for i := 0; i < 4; i++ {
		cb.RecordResult(true)
	}
	if cb.State() != StateClosed {
		t.Fatalf("state = %d, want Closed", cb.State())
	}
}

func TestHalfOpenAfterCooldown(t *testing.T) {
	cb := NewWithConfig(5, 30*time.Second)
	clk := time.Unix(1_000_000, 0)
	cb.now = func() time.Time { return clk }

	for i := 0; i < 5; i++ {
		cb.RecordResult(true)
	}
	if cb.State() != StateOpen {
		t.Fatalf("state = %d, want Open", cb.State())
	}

	// Within cooldown: still blocked.
	clk = clk.Add(29 * time.Second)
	if cb.AllowRequest() {
		t.Fatal("AllowRequest = true before cooldown elapsed")
	}

	// Past cooldown: first call wins the trial and moves to HalfOpen.
	clk = clk.Add(2 * time.Second)
	if !cb.AllowRequest() {
		t.Fatal("AllowRequest = false after cooldown, want true (trial)")
	}
	if cb.State() != StateHalfOpen {
		t.Fatalf("state = %d, want HalfOpen", cb.State())
	}
}

func TestHalfOpenRecoversOnSuccess(t *testing.T) {
	cb := NewWithConfig(5, 30*time.Second)
	clk := time.Unix(1_000_000, 0)
	cb.now = func() time.Time { return clk }

	for i := 0; i < 5; i++ {
		cb.RecordResult(true)
	}
	clk = clk.Add(31 * time.Second)
	cb.AllowRequest() // → HalfOpen

	cb.RecordResult(false) // trial succeeds
	if cb.State() != StateClosed {
		t.Fatalf("state = %d, want Closed after successful trial", cb.State())
	}
}

func TestHalfOpenRetripsOnFailure(t *testing.T) {
	cb := NewWithConfig(5, 30*time.Second)
	clk := time.Unix(1_000_000, 0)
	cb.now = func() time.Time { return clk }

	for i := 0; i < 5; i++ {
		cb.RecordResult(true)
	}
	clk = clk.Add(31 * time.Second)
	cb.AllowRequest() // → HalfOpen

	// A failure in HalfOpen: the streak (5 already) keeps it Open.
	cb.RecordResult(true)
	if cb.State() != StateOpen {
		t.Fatalf("state = %d, want Open after failed trial", cb.State())
	}
}

func TestConcurrentAccessIsRaceFree(t *testing.T) {
	cb := New()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				cb.RecordResult(j%3 == 0)
				_ = cb.AllowRequest()
			}
		}(i)
	}
	wg.Wait()
	// No assertion on final state; -race is the check.
}

func TestLeakyBucketTripsOnInterleavedErrors(t *testing.T) {
	// JANUS-02: 1-in-5 failure pattern (20% error rate) must trip the breaker within 50 requests
	cb := NewWithThresholds(5, 10, 5*time.Second)

	trippedAt := -1
	for req := 1; req <= 100; req++ {
		failed := req%5 == 0
		cb.RecordResult(failed)
		if cb.State() == StateOpen {
			trippedAt = req
			break
		}
	}

	if trippedAt == -1 {
		t.Fatal("breaker never tripped on 20% interleaved errors")
	}
	if trippedAt > 50 {
		t.Fatalf("breaker tripped at %d requests, want <= 50", trippedAt)
	}
	if cb.AllowRequest() {
		t.Fatal("AllowRequest = true immediately after trip, want false")
	}
}

func TestHalfOpenStrictSingleTrial(t *testing.T) {
	// Risk 1 verification: strictly one trial is admitted during HalfOpen
	cb := NewWithConfig(5, 5*time.Second)
	clk := time.Unix(1_000_000, 0)
	cb.now = func() time.Time { return clk }

	// Trip it Open
	for i := 0; i < 5; i++ {
		cb.RecordResult(true)
	}
	if cb.State() != StateOpen {
		t.Fatalf("state = %d, want Open", cb.State())
	}

	// Advance clock past cooldown
	clk = clk.Add(6 * time.Second)

	// First request wins the trial
	if !cb.AllowRequest() {
		t.Fatal("first call after cooldown was rejected, want true")
	}
	if cb.State() != StateHalfOpen {
		t.Fatalf("state = %d, want HalfOpen", cb.State())
	}

	// Any concurrent calls while trial is in flight must be rejected
	for i := 0; i < 10; i++ {
		if cb.AllowRequest() {
			t.Fatalf("concurrent call %d was admitted during HalfOpen while trial in flight", i)
		}
	}

	// Trial completes with success -> recovers to Closed
	cb.RecordResult(false)
	if cb.State() != StateClosed {
		t.Fatalf("state = %d, want Closed after successful trial", cb.State())
	}
	if !cb.AllowRequest() {
		t.Fatal("AllowRequest = false after recovery to Closed, want true")
	}
}
