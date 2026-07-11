package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const (
	readinessSuccessTTL   = 5 * time.Second
	readinessFailureTTL   = 2 * time.Second
	readinessProbeTimeout = 2 * time.Second
)

var errReadinessDraining = errors.New("application is draining")

type readinessProbe func(context.Context) error

// readinessChecker caches readiness results and coalesces dependency refreshes.
// The probe deliberately has a lifetime independent of any individual request.
type readinessChecker struct {
	probe        readinessProbe
	successTTL   time.Duration
	failureTTL   time.Duration
	timeout      time.Duration
	now          func() time.Time
	onTransition func(error)
	draining     atomic.Bool

	mu            sync.Mutex
	cached        bool
	result        error
	expiresAt     time.Time
	observedState bool
	lastReady     bool
	probeMu       sync.Mutex
	activeProbe   *readinessProbeRun
}

type readinessProbeRun struct {
	done chan struct{}
	once sync.Once
	err  error
}

func newReadinessChecker(probe readinessProbe) *readinessChecker {
	return newReadinessCheckerWithConfig(probe, readinessCheckerConfig{
		SuccessTTL: readinessSuccessTTL,
		FailureTTL: readinessFailureTTL,
		Timeout:    readinessProbeTimeout,
	})
}

type readinessCheckerConfig struct {
	SuccessTTL   time.Duration
	FailureTTL   time.Duration
	Timeout      time.Duration
	Now          func() time.Time
	OnTransition func(error)
}

func newReadinessCheckerWithConfig(probe readinessProbe, cfg readinessCheckerConfig) *readinessChecker {
	if cfg.SuccessTTL <= 0 {
		cfg.SuccessTTL = readinessSuccessTTL
	}
	if cfg.FailureTTL <= 0 {
		cfg.FailureTTL = readinessFailureTTL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = readinessProbeTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &readinessChecker{
		probe: probe, successTTL: cfg.SuccessTTL, failureTTL: cfg.FailureTTL,
		timeout: cfg.Timeout, now: cfg.Now, onTransition: cfg.OnTransition,
	}
}

func (c *readinessChecker) Check(ctx context.Context) error {
	if c == nil {
		return errors.New("readiness checker is not configured")
	}
	if c.draining.Load() {
		return errReadinessDraining
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if result, ok := c.cachedResult(); ok {
		if c.draining.Load() {
			return errReadinessDraining
		}
		return result
	}

	run, cached, ok := c.getOrStartProbe()
	if ok {
		return cached
	}
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		c.completeProbe(run, context.DeadlineExceeded, false)
		return run.err
	case <-run.done:
		if c.draining.Load() {
			return errReadinessDraining
		}
		return run.err
	}
}

func (c *readinessChecker) BeginDrain() {
	if c == nil || !c.draining.CompareAndSwap(false, true) {
		return
	}
	c.cacheResult(errReadinessDraining, c.failureTTL)
	c.probeMu.Lock()
	run := c.activeProbe
	c.probeMu.Unlock()
	if run != nil {
		c.completeProbe(run, errReadinessDraining, false)
	}
}

func (c *readinessChecker) cachedResult() (error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.cached || !c.now().Before(c.expiresAt) {
		return nil, false
	}
	return c.result, true
}

func (c *readinessChecker) getOrStartProbe() (*readinessProbeRun, error, bool) {
	c.probeMu.Lock()
	defer c.probeMu.Unlock()
	if c.activeProbe != nil {
		return c.activeProbe, nil, false
	}
	if result, ok := c.cachedResult(); ok {
		return nil, result, true
	}
	run := &readinessProbeRun{done: make(chan struct{})}
	c.activeProbe = run
	go c.executeProbe(run)
	return run, nil, false
}

func (c *readinessChecker) executeProbe(run *readinessProbeRun) {
	probeCtx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	err := errors.New("readiness dependency probe is not configured")
	if c.probe != nil {
		err = c.probe(probeCtx)
	}
	c.completeProbe(run, err, true)
}

func (c *readinessChecker) completeProbe(run *readinessProbeRun, err error, clearActive bool) {
	if run == nil {
		return
	}
	completed := false
	run.once.Do(func() {
		completed = true
		if c.draining.Load() {
			err = errReadinessDraining
		}
		run.err = err
		ttl := c.successTTL
		if err != nil {
			ttl = c.failureTTL
		}
		if clearActive {
			c.probeMu.Lock()
			c.cacheResult(err, ttl)
			if c.activeProbe == run {
				c.activeProbe = nil
			}
			c.probeMu.Unlock()
		} else {
			c.cacheResult(err, ttl)
		}
		close(run.done)
	})
	if clearActive && !completed {
		c.probeMu.Lock()
		if c.activeProbe == run {
			c.activeProbe = nil
		}
		c.probeMu.Unlock()
	}
}

func (c *readinessChecker) cacheResult(err error, ttl time.Duration) {
	notify := false

	c.mu.Lock()
	if c.draining.Load() {
		err = errReadinessDraining
	}
	ready := err == nil
	c.cached = true
	c.result = err
	c.expiresAt = c.now().Add(ttl)
	if !c.observedState {
		c.observedState = true
		c.lastReady = ready
		notify = !ready
	} else if c.lastReady != ready {
		c.lastReady = ready
		notify = true
	}
	onTransition := c.onTransition
	c.mu.Unlock()

	if notify && onTransition != nil {
		onTransition(err)
	}
}
