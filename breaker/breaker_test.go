package breaker

import (
	"context"
	"errors"
	"sync"
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

func (fc *fakeClock) Now() time.Time          { return fc.now.Load().(time.Time) }
func (fc *fakeClock) advance(d time.Duration) { fc.now.Store(fc.Now().Add(d)) }

var ctx = context.Background()

func succeed(context.Context) error { return nil }

func Test_AllSuccess_StaysClosed(t *testing.T) {
	fc := newFakeClock(time.Now())
	b := New(Settings{Clock: fc})

	for i := range 10 {
		if err := b.Execute(ctx, succeed); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if st := b.State(); st != closed {
		t.Fatalf("want closed, got %s", st)
	}
}

func Test_TripsOpenAfterThreshold(t *testing.T) {
	fc := newFakeClock(time.Now())
	const thr = 3
	b := New(Settings{FailureThreshold: thr, OpenTimeout: time.Second, Clock: fc})
	boom := errors.New("boom")

	for i := range thr {
		if err := b.Execute(ctx, func(context.Context) error { return boom }); err != boom {
			t.Fatalf("pre‑trip %d: want boom, got %v", i, err)
		}
	}

	if err := b.Execute(ctx, succeed); !errors.Is(err, ErrOpenState) {
		t.Fatalf("expected ErrOpenState, got %v", err)
	}
	if st := b.State(); st != open {
		t.Fatalf("state=%s, want open", st)
	}
}

func Test_HalfOpenSuccessCloses(t *testing.T) {
	fc := newFakeClock(time.Now())
	b := New(Settings{FailureThreshold: 2, SuccessThreshold: 2, OpenTimeout: time.Second, Clock: fc})
	fail := errors.New("fail")
	for range 2 {
		_ = b.Execute(ctx, func(context.Context) error { return fail })
	}

	fc.advance(time.Second) // move to half‑open

	if err := b.Execute(ctx, succeed); err != nil {
		t.Fatalf("half‑open success 1: %v", err)
	}
	if err := b.Execute(ctx, succeed); err != nil {
		t.Fatalf("half‑open success 2: %v", err)
	}
	if st := b.State(); st != closed {
		t.Fatalf("want closed, got %s", st)
	}
}

func Test_HalfOpenFailureRetrips(t *testing.T) {
	fc := newFakeClock(time.Now())
	b := New(Settings{FailureThreshold: 1, SuccessThreshold: 1, OpenTimeout: time.Second, Clock: fc})
	fail := errors.New("fail")
	_ = b.Execute(ctx, func(context.Context) error { return fail }) // trip
	fc.advance(time.Second)

	if err := b.Execute(ctx, func(context.Context) error { return fail }); err != fail {
		t.Fatalf("expected op error, got %v", err)
	}
	if st := b.State(); st != open {
		t.Fatalf("want open, got %s", st)
	}
}

func Test_OnStateChangeHook(t *testing.T) {
	fc := newFakeClock(time.Now())
	done := make(chan struct{})
	b := New(Settings{
		FailureThreshold: 1,
		OpenTimeout:      time.Second,
		Clock:            fc,
		OnStateChange: func(prev, next State) {
			if next == open {
				done <- struct{}{}
			}
		},
	})
	_ = b.Execute(ctx, func(context.Context) error { return errors.New("fail") }) // trip

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("OnStateChange hook not invoked")
	}
}

func Test_ConcurrentHalfOpenAdmission(t *testing.T) {
	fc := newFakeClock(time.Now())
	const succ = 2
	b := New(Settings{FailureThreshold: 1, SuccessThreshold: succ, OpenTimeout: time.Second, Clock: fc})
	_ = b.Execute(ctx, func(context.Context) error { return errors.New("fail") }) // trip
	fc.advance(time.Second)

	proceed := make(chan struct{})
	started := make(chan struct{}, succ)
	var wg sync.WaitGroup
	var successCnt int32

	op := func(context.Context) error {
		started <- struct{}{}
		<-proceed
		atomic.AddInt32(&successCnt, 1)
		return nil
	}

	for range succ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Execute(ctx, op)
		}()
	}

	for range succ {
		<-started
	}

	if err := b.Execute(ctx, succeed); !errors.Is(err, ErrOpenState) {
		t.Fatalf("expected ErrOpenState for extra probe, got %v", err)
	}

	close(proceed)
	wg.Wait()

	if successCnt != succ {
		t.Fatalf("expected %d successes in half‑open, got %d", succ, successCnt)
	}
	if st := b.State().String(); st != "closed" {
		t.Fatalf("should be closed, got %s", st)
	}
}
