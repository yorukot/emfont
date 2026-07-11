package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadinessCheckerDefaultCacheWindowIsBounded(t *testing.T) {
	checker := newReadinessChecker(func(context.Context) error { return nil })
	if checker.successTTL != 5*time.Second || checker.failureTTL != 2*time.Second {
		t.Fatalf("default readiness TTLs = success %s, failure %s", checker.successTTL, checker.failureTTL)
	}
}

func TestReadinessCheckerCoalescesConcurrentRefreshes(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	checker := newReadinessCheckerWithConfig(func(context.Context) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}, readinessCheckerConfig{Timeout: time.Second})

	const callers = 32
	start := make(chan struct{})
	errs := make(chan error, callers)
	var callersWG sync.WaitGroup
	callersWG.Add(callers)
	for range callers {
		go func() {
			defer callersWG.Done()
			<-start
			errs <- checker.Check(context.Background())
		}()
	}
	close(start)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("dependency probe did not start")
	}
	close(release)
	callersWG.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("dependency probes = %d, want 1", got)
	}
}

func TestReadinessCheckerRefreshesAfterSuccessTTL(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	checker := newReadinessCheckerWithConfig(func(context.Context) error {
		calls.Add(1)
		return nil
	}, readinessCheckerConfig{
		SuccessTTL: 10 * time.Second,
		FailureTTL: time.Second,
		Timeout:    time.Second,
		Now:        func() time.Time { return now },
	})

	if err := checker.Check(context.Background()); err != nil {
		t.Fatalf("initial Check: %v", err)
	}
	now = now.Add(9 * time.Second)
	if err := checker.Check(context.Background()); err != nil {
		t.Fatalf("cached Check: %v", err)
	}
	now = now.Add(time.Second)
	if err := checker.Check(context.Background()); err != nil {
		t.Fatalf("refreshed Check: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("dependency probes = %d, want 2", got)
	}
}

func TestReadinessCheckerCachesFailuresForFailureTTL(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	want := errors.New("postgres unavailable")
	var calls atomic.Int32
	checker := newReadinessCheckerWithConfig(func(context.Context) error {
		calls.Add(1)
		return want
	}, readinessCheckerConfig{
		SuccessTTL: time.Minute,
		FailureTTL: 2 * time.Second,
		Timeout:    time.Second,
		Now:        func() time.Time { return now },
	})

	if err := checker.Check(context.Background()); !errors.Is(err, want) {
		t.Fatalf("initial Check error = %v, want %v", err, want)
	}
	now = now.Add(time.Second)
	if err := checker.Check(context.Background()); !errors.Is(err, want) {
		t.Fatalf("cached Check error = %v, want %v", err, want)
	}
	now = now.Add(time.Second)
	if err := checker.Check(context.Background()); !errors.Is(err, want) {
		t.Fatalf("refreshed Check error = %v, want %v", err, want)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("dependency probes = %d, want 2", got)
	}
}

func TestReadinessCheckerAppliesDedicatedProbeTimeout(t *testing.T) {
	probeStarted := make(chan struct{})
	probeRelease := make(chan struct{})
	probeFinished := make(chan struct{})
	checker := newReadinessCheckerWithConfig(func(ctx context.Context) error {
		close(probeStarted)
		<-probeRelease
		close(probeFinished)
		return nil
	}, readinessCheckerConfig{Timeout: 20 * time.Millisecond})

	err := checker.Check(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Check error = %v, want deadline exceeded", err)
	}
	select {
	case <-probeStarted:
	default:
		t.Fatal("dependency probe did not start")
	}
	close(probeRelease)
	select {
	case <-probeFinished:
	case <-time.After(time.Second):
		t.Fatal("timed out dependency probe did not finish")
	}
}

func TestReadinessCheckerPermanentlyBlockedProbeRemainsSingleOutstandingCall(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	probeExited := make(chan struct{})
	checker := newReadinessCheckerWithConfig(func(context.Context) error {
		if calls.Add(1) != 1 {
			t.Error("started more than one dependency probe")
		}
		<-release
		close(probeExited)
		return nil
	}, readinessCheckerConfig{
		Timeout: 10 * time.Millisecond, FailureTTL: time.Millisecond,
	})

	const callers = 64
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			if err := checker.Check(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Check error = %v, want deadline exceeded", err)
			}
		}()
	}
	wg.Wait()
	time.Sleep(2 * time.Millisecond)
	for range 10 {
		if err := checker.Check(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("repeated Check error = %v, want deadline exceeded", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("dependency probe calls = %d, want exactly one", got)
	}

	close(release)
	select {
	case <-probeExited:
	case <-time.After(time.Second):
		t.Fatal("blocked dependency probe did not exit after release")
	}
}

func TestReadinessCheckerCallerCancellationDoesNotCancelRefresh(t *testing.T) {
	probeStarted := make(chan struct{})
	probeRelease := make(chan struct{})
	probeResult := make(chan error, 1)
	checker := newReadinessCheckerWithConfig(func(ctx context.Context) error {
		close(probeStarted)
		<-probeRelease
		probeResult <- ctx.Err()
		return nil
	}, readinessCheckerConfig{Timeout: time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- checker.Check(ctx) }()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("dependency probe did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Check error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled caller did not return promptly")
	}
	close(probeRelease)
	select {
	case err := <-probeResult:
		if err != nil {
			t.Fatalf("probe context error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dependency probe did not finish")
	}
	if err := checker.Check(context.Background()); err != nil {
		t.Fatalf("cached Check: %v", err)
	}
}

func TestReadinessCheckerBeginDrainImmediatelyOverridesCachedSuccess(t *testing.T) {
	var calls atomic.Int32
	checker := newReadinessChecker(func(context.Context) error {
		calls.Add(1)
		return nil
	})
	if err := checker.Check(context.Background()); err != nil {
		t.Fatalf("initial Check: %v", err)
	}

	checker.BeginDrain()
	if err := checker.Check(context.Background()); !errors.Is(err, errReadinessDraining) {
		t.Fatalf("draining Check error = %v, want %v", err, errReadinessDraining)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("dependency probes after drain = %d, want 1", got)
	}
}

func TestReadinessCheckerBeginDrainWinsInFlightRefresh(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	checker := newReadinessCheckerWithConfig(func(context.Context) error {
		close(started)
		<-release
		return nil
	}, readinessCheckerConfig{Timeout: time.Second})

	firstResult := make(chan error, 1)
	go func() { firstResult <- checker.Check(context.Background()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("dependency probe did not start")
	}
	checker.BeginDrain()

	if err := checker.Check(context.Background()); !errors.Is(err, errReadinessDraining) {
		t.Fatalf("Check during refresh = %v, want drain error", err)
	}
	close(release)
	select {
	case err := <-firstResult:
		if !errors.Is(err, errReadinessDraining) {
			t.Fatalf("in-flight Check error = %v, want drain error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight Check did not return")
	}
}

func TestReadinessCheckerNotifiesOnlyOnStateTransitions(t *testing.T) {
	now := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	dependencyErr := errors.New("database unavailable")
	var ready atomic.Bool
	var transitionsMu sync.Mutex
	var transitions []error
	checker := newReadinessCheckerWithConfig(func(context.Context) error {
		if ready.Load() {
			return nil
		}
		return dependencyErr
	}, readinessCheckerConfig{
		SuccessTTL: time.Second,
		FailureTTL: time.Second,
		Timeout:    time.Second,
		Now:        func() time.Time { return now },
		OnTransition: func(err error) {
			transitionsMu.Lock()
			transitions = append(transitions, err)
			transitionsMu.Unlock()
		},
	})

	for range 2 {
		if err := checker.Check(context.Background()); !errors.Is(err, dependencyErr) {
			t.Fatalf("failed Check = %v", err)
		}
	}
	now = now.Add(time.Second)
	if err := checker.Check(context.Background()); !errors.Is(err, dependencyErr) {
		t.Fatalf("refreshed failed Check = %v", err)
	}
	ready.Store(true)
	now = now.Add(time.Second)
	if err := checker.Check(context.Background()); err != nil {
		t.Fatalf("recovered Check: %v", err)
	}

	transitionsMu.Lock()
	defer transitionsMu.Unlock()
	if len(transitions) != 2 || !errors.Is(transitions[0], dependencyErr) || transitions[1] != nil {
		t.Fatalf("transitions = %#v, want failure then recovery", transitions)
	}
}
