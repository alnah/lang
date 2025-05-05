package breaker

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrOpenState signals that the circuit is currently open and a call was short-circuited.
var ErrOpenState = errors.New("circuit breaker is open")

// Operation represents a unit of work that can succeed or fail.
type Operation func(ctx context.Context) error

// State models the finite-state machine of the breaker.
type State int

const (
	closed   State = iota // normal operation; everything flows
	open                  // short-circuiting; no calls allowed
	halfOpen              // probing; limited calls allowed
)

// String provides a human-readable form.
func (s State) String() string {
	switch s {
	case closed:
		return "closed"
	case open:
		return "open"
	case halfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Clock abstracts time for deterministic tests.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Settings configures the breaker.
type Settings struct {
	FailureThreshold int           // consecutive failures to trip from closed → open
	SuccessThreshold int           // consecutive successes to recover from half-open → closed
	OpenTimeout      time.Duration // minimum open period before probing
	Clock            Clock         // dependency-injected time source
	OnStateChange    func(prev, next State)
}

// Breaker implements Execute with concurrency-safe state transitions.
type Breaker struct {
	s         Settings
	mu        sync.Mutex
	state     State
	failures  int
	successes int
	allowed   int       // remaining probes allowed in half-open
	openUntil time.Time // gate to leave open state
}

// New returns a Breaker using sane defaults when a field is zero.
func New(s Settings) *Breaker {
	if s.FailureThreshold < 1 {
		s.FailureThreshold = 5
	}
	if s.SuccessThreshold < 1 {
		s.SuccessThreshold = 1
	}
	if s.OpenTimeout == 0 {
		s.OpenTimeout = 30 * time.Second
	}
	if s.Clock == nil {
		s.Clock = realClock{}
	}
	return &Breaker{s: s, state: closed}
}

// State returns the current breaker state in a race-safe way.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Execute runs op if policy allows; ErrOpenState is returned when blocked.
func (b *Breaker) Execute(ctx context.Context, op Operation) error {
	if op == nil {
		return errors.New("nil operation")
	}

	// admission phase
	b.mu.Lock()
	now := b.s.Clock.Now()

	switch b.state {
	case open:
		if now.Before(b.openUntil) {
			b.mu.Unlock()
			return ErrOpenState
		}
		// first caller after the timeout transitions to half-open
		b.transitionLocked(halfOpen)
		b.failures, b.successes = 0, 0
		// allow exactly SuccessThreshold probes; success accounting happens later
		b.allowed = b.s.SuccessThreshold
		b.allowed--

	case halfOpen:
		if b.allowed <= 0 {
			b.mu.Unlock()
			return ErrOpenState
		}
		b.allowed--
	}
	b.mu.Unlock()

	// run operation outside the lock
	err := op(ctx)

	// feedback phase
	b.mu.Lock()
	defer b.mu.Unlock()

	if err == nil { // success path
		switch b.state {
		case halfOpen:
			b.successes++
			if b.successes >= b.s.SuccessThreshold {
				b.transitionLocked(closed)
				b.failures = 0
			}
		case closed:
			b.failures = 0
		}
		return nil
	}

	// failure path
	switch b.state {
	case halfOpen:
		b.tripLocked()
	case closed:
		b.failures++
		if b.failures >= b.s.FailureThreshold {
			b.tripLocked()
		}
	}

	return err
}

// tripLocked puts the breaker into open state (caller holds the lock).
func (b *Breaker) tripLocked() {
	b.openUntil = b.s.Clock.Now().Add(b.s.OpenTimeout)
	b.transitionLocked(open)
	b.failures, b.successes, b.allowed = 0, 0, 0
}

// transitionLocked moves from the current state to next (caller holds the lock).
func (b *Breaker) transitionLocked(next State) {
	if b.state == next {
		return
	}
	prev := b.state
	b.state = next
	if b.s.OnStateChange != nil {
		go func() { // fire the hook safely out of band
			defer func() { _ = recover() }()
			b.s.OnStateChange(prev, next)
		}()
	}
}
