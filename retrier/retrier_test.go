package retrier

import (
	"context"
	"errors"
	"math/rand"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct{ now atomic.Value }

func newFakeClock(t time.Time) *fakeClock {
	fc := &fakeClock{}
	fc.now.Store(t)
	return fc
}

func (fc *fakeClock) Now() time.Time        { return fc.now.Load().(time.Time) }
func (fc *fakeClock) Sleep(d time.Duration) { fc.now.Store(fc.Now().Add(d)) }

func TestDo_NilOperation(t *testing.T) {
	r := New()
	err := r.Do(context.Background(), nil)
	if err == nil || err.Error() != "nil operation" {
		t.Fatalf("want nil operation error, got %v", err)
	}
}

func TestDo_SingleSuccess(t *testing.T) {
	called := 0
	r := New(WithClock(newFakeClock(time.Now())), WithJitter(NoJitter))
	if err := r.Do(context.Background(), func(ctx context.Context) error { called++; return nil }); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called != 1 {
		t.Fatalf("op called %d times, want 1", called)
	}
}

func TestDo_RetryUntilSuccess(t *testing.T) {
	fc := newFakeClock(time.Now())
	attempts := 0
	var delays []time.Duration
	r := New(
		WithMaxAttempts(4),
		WithBaseDelay(time.Second),
		WithJitter(NoJitter),
		WithClock(fc),
		WithOnRetry(func(a int, err error, d time.Duration) { delays = append(delays, d) }),
	)

	err := r.Do(context.Background(), func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("fail")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if attempts != 3 {
		t.Fatalf("want 3 attempts, got %d", attempts)
	}
	if len(delays) != 2 || delays[0] != time.Second || delays[1] != 2*time.Second {
		t.Fatalf("unexpected delays %v", delays)
	}
}

func TestRetry_StopsOnNonRetryable(t *testing.T) {
	fc := newFakeClock(time.Now())
	r := New(WithMaxAttempts(5), WithClock(fc))
	perm := errors.New("perm")
	err := r.Retry(context.Background(), func(ctx context.Context) error { return perm }, func(e error) bool { return false })
	if err != perm {
		t.Fatalf("expected %v, got %v", perm, err)
	}
}

func TestJitterStrategies(t *testing.T) {
	rnd := rand.New(rand.NewSource(42))
	d := 100 * time.Millisecond
	if n := NoJitter(d, rnd); n != d {
		t.Fatalf("NoJitter: want %v, got %v", d, n)
	}
	if f := FullJitter(d, rnd); f < 0 || f > d {
		t.Fatalf("FullJitter out of range: %v", f)
	}
	if e := EqualJitter(d, rnd); e < d/2 || e > d {
		t.Fatalf("EqualJitter out of range: %v", e)
	}
}
