package retrier

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"
)

// Operation is a unit of work that may fail transiently.
type Operation func(ctx context.Context) error

// RetryError is returned when all retry attempts are exhausted.
type RetryError struct {
	Attempts int   // total attempts made (≥ maxAttempts)
	Err      error // last error returned by the operation
}

func (e *RetryError) Error() string {
	return fmt.Sprintf("after %d attempt(s): %v", e.Attempts, e.Err)
}

func (e *RetryError) Unwrap() error { return e.Err }

// Clock abstracts sleeping/time for deterministic tests.
// realClock satisfies it in production.
type Clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

type realClock struct{}

func (realClock) Now() time.Time        { return time.Now() }
func (realClock) Sleep(d time.Duration) { time.Sleep(d) }

// JitterStrategy randomises the back-off delay.
type JitterStrategy func(d time.Duration, rnd *rand.Rand) time.Duration

var (
	// NoJitter leaves the delay untouched.
	NoJitter JitterStrategy = func(d time.Duration, _ *rand.Rand) time.Duration { return d }

	// FullJitter (AWS style) picks uniformly in [0, d].
	FullJitter JitterStrategy = func(d time.Duration, rnd *rand.Rand) time.Duration {
		if d <= 0 {
			return 0
		}
		return time.Duration(rnd.Int63n(int64(d) + 1))
	}

	// EqualJitter (Google style) picks uniformly in [d/2, d].
	EqualJitter JitterStrategy = func(d time.Duration, rnd *rand.Rand) time.Duration {
		if d <= 0 {
			return 0
		}
		half := d / 2
		return half + time.Duration(rnd.Int63n(int64(half)+1))
	}
)

type Option func(*Retrier)

func WithMaxAttempts(n int) Option         { return func(r *Retrier) { r.maxAttempts = n } }
func WithBaseDelay(d time.Duration) Option { return func(r *Retrier) { r.baseDelay = d } }
func WithMultiplier(m float64) Option      { return func(r *Retrier) { r.multiplier = m } }
func WithMaxDelay(d time.Duration) Option  { return func(r *Retrier) { r.maxDelay = d } }
func WithJitter(js JitterStrategy) Option  { return func(r *Retrier) { r.jitter = js } }
func WithRand(src rand.Source) Option      { return func(r *Retrier) { r.rnd = rand.New(src) } }
func WithClock(c Clock) Option             { return func(r *Retrier) { r.clock = c } }
func WithOnRetry(fn func(attempt int, err error, nextDelay time.Duration)) Option {
	return func(r *Retrier) { r.onRetry = fn }
}

// Retrier encapsulates retry behaviour with exponential back-off.
type Retrier struct {
	maxAttempts int
	baseDelay   time.Duration
	multiplier  float64
	maxDelay    time.Duration
	jitter      JitterStrategy
	onRetry     func(attempt int, err error, nextDelay time.Duration)

	rnd   *rand.Rand
	rndMu sync.Mutex
	clock Clock
}

const (
	defaultAttempts   = 3
	defaultBaseDelay  = 100 * time.Millisecond
	defaultMultiplier = 2.0
	defaultMaxDelay   = 30 * time.Second
)

// New builds a Retrier with sane defaults then applies opts.
func New(opts ...Option) *Retrier {
	r := &Retrier{
		maxAttempts: defaultAttempts,
		baseDelay:   defaultBaseDelay,
		multiplier:  defaultMultiplier,
		maxDelay:    defaultMaxDelay,
		jitter:      FullJitter,
		rnd:         rand.New(rand.NewSource(time.Now().UnixNano())),
		clock:       realClock{},
	}
	for _, o := range opts {
		o(r)
	}
	// ensure fields are valid
	if r.maxAttempts < 1 {
		r.maxAttempts = 1
	}
	if r.multiplier < 1 {
		r.multiplier = 1
	}
	if r.baseDelay < 0 {
		r.baseDelay = 0
	}
	if r.maxDelay <= 0 {
		r.maxDelay = defaultMaxDelay
	}
	if r.jitter == nil {
		r.jitter = NoJitter
	}
	if r.clock == nil {
		r.clock = realClock{}
	}
	if r.rnd == nil {
		r.rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return r
}

// Do executes op, retrying on every non-nil error until success or maxAttempts.
func (r *Retrier) Do(ctx context.Context, op Operation) error {
	return r.Retry(ctx, op, func(error) bool { return true })
}

// Retry executes op while isRetryable(err) == true.
func (r *Retrier) Retry(ctx context.Context, op Operation, isRetryable func(error) bool) error {
	if op == nil {
		return errors.New("nil operation")
	}
	if isRetryable == nil {
		isRetryable = func(error) bool { return true }
	}

	delay := r.baseDelay
	var lastErr error

	for attempt := 1; attempt <= r.maxAttempts; attempt++ {
		lastErr = op(ctx)
		if lastErr == nil {
			return nil
		}
		// stop if non-retryable
		if !isRetryable(lastErr) {
			return lastErr
		}
		// stop if hit attempt limit
		if attempt == r.maxAttempts {
			return &RetryError{Attempts: attempt, Err: lastErr}
		}

		// next delay with jitter
		nextDelay := r.jitter(delay, r.safeRand())
		if r.onRetry != nil {
			r.onRetry(attempt, lastErr, nextDelay)
		}

		// wait or bail if context canceled
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			r.clock.Sleep(nextDelay)
		}

		// exponential back-off (overflow-safe)
		if delay >= r.maxDelay {
			delay = r.maxDelay
		} else {
			mult := float64(delay) * r.multiplier
			if mult > float64(math.MaxInt64) {
				delay = r.maxDelay
			} else {
				delay = time.Duration(math.Min(float64(r.maxDelay), mult))
			}
		}
	}

	// theoretically unreachable
	return &RetryError{Attempts: r.maxAttempts, Err: lastErr}
}

// safeRand returns r.rnd with concurrency protection.
func (r *Retrier) safeRand() *rand.Rand {
	r.rndMu.Lock()
	defer r.rndMu.Unlock()
	return r.rnd
}
