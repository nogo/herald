package deployer

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLimiter_BoundsProductionAndPreviewTogether submits more fake deployment
// requests than the limit, split across "production" and "preview" callers
// sharing one Limiter (mirroring DeployAsync and PreviewManager.Deploy in the
// real daemon), and proves the number of concurrently active fake deployments
// never exceeds the limit while still reaching it.
func TestLimiter_BoundsProductionAndPreviewTogether(t *testing.T) {
	const limit = 4
	const productionRequests = 12
	const previewRequests = 12

	l := NewLimiter(limit)

	// hold blocks every admitted fake deployment until the test releases them,
	// so admission is forced to saturate the limit before any unit of work
	// completes and frees a slot.
	hold := make(chan struct{})

	var mu sync.Mutex
	var active, peak int
	var exceeded atomic.Bool

	fakeDeploy := func() {
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		if active > limit {
			exceeded.Store(true)
		}
		mu.Unlock()

		<-hold // fake deployment execution: stays "running" until told to finish

		mu.Lock()
		active--
		mu.Unlock()
	}

	submit := func(wg *sync.WaitGroup) {
		defer wg.Done()
		release, err := l.Acquire(context.Background())
		if err != nil {
			t.Errorf("acquire: %v", err)
			return
		}
		defer release()
		fakeDeploy()
	}

	var wg sync.WaitGroup
	for range productionRequests {
		wg.Add(1)
		go submit(&wg)
	}
	for range previewRequests {
		wg.Add(1)
		go submit(&wg)
	}

	// Wait for admission to saturate the limit before releasing any work, so the
	// test proves capacity is actually used, not just never exceeded by chance.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		reached := active >= limit
		mu.Unlock()
		if reached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("active fake deployments never reached the limit; admission may be stuck")
		}
		time.Sleep(time.Millisecond)
	}

	close(hold)
	wg.Wait()

	if exceeded.Load() {
		t.Fatalf("active concurrent fake deployments exceeded the limit of %d", limit)
	}
	if peak != limit {
		t.Fatalf("peak concurrent = %d, want exactly %d", peak, limit)
	}
}

// TestLimiter_CloseStopsAdmission verifies that once Close is called, no
// further work is admitted even if a slot is free, so a callback racing
// shutdown cannot start a build after the daemon considers deployment work
// finished.
func TestLimiter_CloseStopsAdmission(t *testing.T) {
	l := NewLimiter(4)
	l.Close()

	release, err := l.Acquire(context.Background())
	if err != ErrLimiterClosed {
		t.Fatalf("Acquire after Close: err = %v, want ErrLimiterClosed", err)
	}
	release() // must be safe to call even on the no-op returned for a rejected acquire
}

// TestLimiter_ReleasedOnCancellation verifies that a caller waiting for
// capacity which is instead cancelled via ctx does not leak a slot: the slot
// it never obtained must not still be reserved for it.
func TestLimiter_ReleasedOnCancellation(t *testing.T) {
	l := NewLimiter(1)

	hold := make(chan struct{})
	held, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	go func() {
		<-hold
		held()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := l.Acquire(ctx)
		done <- err
	}()

	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Acquire on cancelled ctx: err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire did not return after ctx cancellation")
	}

	close(hold) // release the held slot

	// The cancelled waiter must not have consumed a slot: a fresh Acquire
	// should succeed immediately now that the only real holder released.
	release, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire after cancellation cleaned up: %v", err)
	}
	release()
}
