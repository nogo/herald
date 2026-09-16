package deployer

import (
	"context"
	"errors"
	"sync"
)

// MaxConcurrentDeployments bounds how many actual production and preview
// deployments may run at once across the daemon, so a burst of matched stacks
// or preview branches cannot spawn unbounded concurrent compose/git runs.
const MaxConcurrentDeployments = 4

// ErrLimiterClosed is returned by Acquire once the daemon has stopped
// admitting new deployment work, typically during shutdown.
var ErrLimiterClosed = errors.New("deploy limiter closed")

// Limiter bounds simultaneous actual deployment work (as opposed to the
// webhook callbacks that trigger it) and tracks it so shutdown can wait for
// admitted work to drain. It is shared by production deploys and preview
// deploys so the two compete for the same fixed pool of capacity.
type Limiter struct {
	sem chan struct{}
	wg  sync.WaitGroup

	mu     sync.Mutex
	closed bool
}

// NewLimiter returns a Limiter that admits at most n simultaneous deployments.
func NewLimiter(n int) *Limiter {
	return &Limiter{sem: make(chan struct{}, n)}
}

// Acquire blocks until a capacity slot is free, ctx is done, or the limiter
// has been closed. On success it returns a release func that must be called
// exactly once, however the caller's work concludes — success, failure, or
// cancellation — to return the slot; the deployment counts as active from
// Acquire until release is called.
func (l *Limiter) Acquire(ctx context.Context) (release func(), err error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return func() {}, ErrLimiterClosed
	}
	l.wg.Add(1)
	l.mu.Unlock()

	select {
	case l.sem <- struct{}{}:
		return func() {
			<-l.sem
			l.wg.Done()
		}, nil
	case <-ctx.Done():
		l.wg.Done()
		return func() {}, ctx.Err()
	}
}

// Close stops the limiter from admitting new work; callers already holding a
// slot are unaffected and must still call their release func.
func (l *Limiter) Close() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
}

// Wait blocks until every admitted deployment has released its slot. Call it
// after Close so admission cannot race a caller into starting new work while
// Wait is observing the count draining to zero.
func (l *Limiter) Wait() {
	l.wg.Wait()
}
