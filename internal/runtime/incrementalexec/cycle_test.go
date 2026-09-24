package incrementalexec_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
)

type fakeOneShot struct {
	calls int64
	fn    func(callIndex int, ctx context.Context) (incrementalexec.Result, error)
}

func (f *fakeOneShot) ExecuteOne(ctx context.Context) (incrementalexec.Result, error) {
	i := int(atomic.AddInt64(&f.calls, 1) - 1)
	if f.fn == nil {
		return incrementalexec.Result{}, incrementalexec.ErrNoEligibleWork
	}
	return f.fn(i, ctx)
}

func (f *fakeOneShot) callCount() int { return int(atomic.LoadInt64(&f.calls)) }

func p5Selected(seq int64) incrementalexec.Result {
	return incrementalexec.Result{
		Selected: true, RootID: "root", ScopeKey: "/scope", SelectedVersion: 1,
		ClaimedSignalSeq: seq, FinalWorkState: state.WorkVerified,
	}
}

func p5Runner(t *testing.T, one incrementalexec.OneShotExecutor) *incrementalexec.CycleRunner {
	t.Helper()
	r, err := incrementalexec.NewCycleRunner(one)
	if err != nil {
		t.Fatalf("new cycle runner: %v", err)
	}
	return r
}

func TestP5CycleConfigBounds(t *testing.T) {
	invalid := []incrementalexec.CycleConfig{
		{MaxItems: 0, MaxWallTime: time.Second},
		{MaxItems: 6, MaxWallTime: time.Second},
		{MaxItems: 1, MaxWallTime: 0},
		{MaxItems: 1, MaxWallTime: -time.Second},
		{MaxItems: 1, MaxWallTime: 61 * time.Second},
	}
	for _, cfg := range invalid {
		one := &fakeOneShot{}
		res, err := p5Runner(t, one).Run(context.Background(), cfg)
		if err == nil {
			t.Fatalf("config %+v must be rejected", cfg)
		}
		if one.callCount() != 0 {
			t.Fatalf("invalid config %+v must invoke no P4 item, got %d", cfg, one.callCount())
		}
		if res.Invocations != 0 {
			t.Fatalf("invalid config %+v must report zero invocations, got %d", cfg, res.Invocations)
		}
	}

	valid := []incrementalexec.CycleConfig{
		{MaxItems: 1, MaxWallTime: time.Second},
		{MaxItems: 5, MaxWallTime: incrementalexec.MaxCycleWallTime},
	}
	for _, cfg := range valid {
		if err := cfg.Validate(); err != nil {
			t.Fatalf("valid config %+v rejected: %v", cfg, err)
		}
	}
}

func TestP5CycleNewCycleRunnerRequiresExecutor(t *testing.T) {
	if _, err := incrementalexec.NewCycleRunner(nil); err == nil {
		t.Fatal("nil one-shot executor must be rejected")
	}
}

func TestP5CycleNoEligibleWork(t *testing.T) {
	one := &fakeOneShot{fn: func(int, context.Context) (incrementalexec.Result, error) {
		return incrementalexec.Result{}, incrementalexec.ErrNoEligibleWork
	}}
	res, err := p5Runner(t, one).Run(context.Background(),
		incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: time.Minute})
	if err != nil {
		t.Fatalf("no eligible work is a normal stop, got %v", err)
	}
	if res.StopReason != incrementalexec.StopNoEligibleWork {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if one.callCount() != 1 || res.Invocations != 1 {
		t.Fatalf("exactly one invocation required, got calls=%d invocations=%d", one.callCount(), res.Invocations)
	}
	if res.SelectedItems != 0 || res.Succeeded != 0 || res.Failed != 0 {
		t.Fatalf("no-work counts must be zero, got %+v", res)
	}
}

func TestP5CycleMaxItemsHardStop(t *testing.T) {
	for _, n := range []int{1, 5} {
		one := &fakeOneShot{fn: func(i int, _ context.Context) (incrementalexec.Result, error) {
			return p5Selected(int64(i + 1)), nil
		}}
		res, err := p5Runner(t, one).Run(context.Background(),
			incrementalexec.CycleConfig{MaxItems: n, MaxWallTime: time.Minute})
		if err != nil {
			t.Fatalf("N=%d: %v", n, err)
		}
		if res.StopReason != incrementalexec.StopMaxItems {
			t.Fatalf("N=%d: stop reason = %s", n, res.StopReason)
		}
		if one.callCount() != n {
			t.Fatalf("N=%d: expected exactly %d calls, got %d", n, n, one.callCount())
		}
		if res.SelectedItems != n || res.Succeeded != n || res.Failed != 0 {
			t.Fatalf("N=%d: counts = %+v", n, res)
		}
	}
}

func TestP5CycleItemLocalFailureContinues(t *testing.T) {
	for _, class := range []state.ErrorClass{state.ErrorAuthOrPermission, state.ErrorThrottled} {
		one := &fakeOneShot{fn: func(i int, _ context.Context) (incrementalexec.Result, error) {
			switch i {
			case 0:
				c := class
				res := p5Selected(7)
				res.FailureClass = &c
				res.FinalWorkState = state.WorkBlocked
				return res, p5ScannerFailedErr(class)
			case 1:
				return p5Selected(8), nil
			default:
				return incrementalexec.Result{}, incrementalexec.ErrNoEligibleWork
			}
		}}
		res, err := p5Runner(t, one).Run(context.Background(),
			incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: time.Minute})
		if err != nil {
			t.Fatalf("%s: item-local failure must not stop the cycle, got %v", class, err)
		}
		if res.StopReason != incrementalexec.StopNoEligibleWork {
			t.Fatalf("%s: stop reason = %s", class, res.StopReason)
		}
		if res.Invocations != 3 || res.SelectedItems != 2 || res.Succeeded != 1 || res.Failed != 1 {
			t.Fatalf("%s: counts = %+v", class, res)
		}
	}
}

// p5ScannerFailedErr mirrors the P4 error contract: ErrScannerFailed wrapping a
// durable item-local class.
func p5ScannerFailedErr(class state.ErrorClass) error {
	return fmt.Errorf("%w: class=%s", incrementalexec.ErrScannerFailed, class)
}

func TestP5CycleInternalStopsImmediately(t *testing.T) {
	one := &fakeOneShot{fn: func(i int, _ context.Context) (incrementalexec.Result, error) {
		if i > 0 {
			return p5Selected(int64(i + 1)), nil
		}
		c := state.ErrorInternal
		res := p5Selected(7)
		res.FailureClass = &c
		res.FinalWorkState = state.WorkRetryWait
		return res, p5ScannerFailedErr(state.ErrorInternal)
	}}
	res, err := p5Runner(t, one).Run(context.Background(),
		incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: time.Minute})
	if err == nil {
		t.Fatal("INTERNAL item failure must return a non-nil cycle error")
	}
	if res.StopReason != incrementalexec.StopInternalItemFailure {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if one.callCount() != 1 || res.Invocations != 1 {
		t.Fatalf("must stop before a second item, calls=%d", one.callCount())
	}
	if res.Failed != 1 || res.SelectedItems != 1 {
		t.Fatalf("counts = %+v", res)
	}
}

func TestP5CycleMissingFailureClassFailsClosed(t *testing.T) {
	one := &fakeOneShot{fn: func(int, context.Context) (incrementalexec.Result, error) {
		// ErrScannerFailed with no FailureClass on the result: classless.
		return p5Selected(7), incrementalexec.ErrScannerFailed
	}}
	res, err := p5Runner(t, one).Run(context.Background(),
		incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: time.Minute})
	if err == nil {
		t.Fatal("missing FailureClass must fail closed")
	}
	if res.StopReason != incrementalexec.StopSystemicError {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if one.callCount() != 1 {
		t.Fatalf("must stop immediately, calls=%d", one.callCount())
	}
	if res.Failed != 0 {
		t.Fatalf("a classless failure is not a durable item failure, got Failed=%d", res.Failed)
	}
}

func TestP5CycleStaleSelectionStops(t *testing.T) {
	one := &fakeOneShot{fn: func(int, context.Context) (incrementalexec.Result, error) {
		return p5Selected(0), incrementalexec.ErrStaleSelection
	}}
	res, err := p5Runner(t, one).Run(context.Background(),
		incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: time.Minute})
	if !errors.Is(err, incrementalexec.ErrStaleSelection) {
		t.Fatalf("want ErrStaleSelection, got %v", err)
	}
	if res.StopReason != incrementalexec.StopStaleSelection {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if one.callCount() != 1 {
		t.Fatalf("stale selection must not reselect in the same cycle, calls=%d", one.callCount())
	}
	if res.SelectedItems != 1 || res.Failed != 0 || res.Succeeded != 0 {
		t.Fatalf("counts = %+v", res)
	}
}

func TestP5CycleCompletionFailureStops(t *testing.T) {
	one := &fakeOneShot{fn: func(int, context.Context) (incrementalexec.Result, error) {
		return p5Selected(7), incrementalexec.ErrCompletionFailed
	}}
	res, err := p5Runner(t, one).Run(context.Background(),
		incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: time.Minute})
	if !errors.Is(err, incrementalexec.ErrCompletionFailed) {
		t.Fatalf("want ErrCompletionFailed, got %v", err)
	}
	if res.StopReason != incrementalexec.StopSystemicError {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if one.callCount() != 1 {
		t.Fatalf("must stop immediately, calls=%d", one.callCount())
	}
}

func TestP5CycleUnexpectedErrorStops(t *testing.T) {
	one := &fakeOneShot{fn: func(int, context.Context) (incrementalexec.Result, error) {
		return p5Selected(7), errors.New("unexpected store error")
	}}
	res, err := p5Runner(t, one).Run(context.Background(),
		incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: time.Minute})
	if err == nil {
		t.Fatal("unexpected error must stop the cycle")
	}
	if res.StopReason != incrementalexec.StopSystemicError {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if one.callCount() != 1 {
		t.Fatalf("must stop immediately, calls=%d", one.callCount())
	}
}

func TestP5CycleWallBudgetBeforeNextItem(t *testing.T) {
	// The first item ignores the context and finishes after the cycle deadline.
	// The runner must notice the expired cycle context before the next call and
	// report MAX_WALL_TIME instead of a systemic selector error.
	one := &fakeOneShot{fn: func(i int, _ context.Context) (incrementalexec.Result, error) {
		time.Sleep(60 * time.Millisecond)
		return p5Selected(int64(i + 1)), nil
	}}
	res, err := p5Runner(t, one).Run(context.Background(),
		incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 25 * time.Millisecond})
	if err != nil {
		t.Fatalf("wall-budget exhaustion is a normal bounded stop, got %v", err)
	}
	if res.StopReason != incrementalexec.StopMaxWallTime {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if one.callCount() != 1 || res.Invocations != 1 {
		t.Fatalf("no second item may start, calls=%d", one.callCount())
	}
	if res.Succeeded != 1 {
		t.Fatalf("first item succeeded, counts = %+v", res)
	}
	if res.InterruptedInFlight {
		t.Fatal("a completed item must not be reported as interrupted")
	}
}

func TestP5CycleWallBudgetDuringItemInterrupts(t *testing.T) {
	one := &fakeOneShot{fn: func(_ int, ctx context.Context) (incrementalexec.Result, error) {
		<-ctx.Done()
		return p5Selected(7), ctx.Err()
	}}
	res, err := p5Runner(t, one).Run(context.Background(),
		incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("cycle wall deadline is a normal bounded stop, got %v", err)
	}
	if res.StopReason != incrementalexec.StopMaxWallTime {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if !res.InterruptedInFlight {
		t.Fatal("a cancelled claimed item must report InterruptedInFlight=true")
	}
	if one.callCount() != 1 {
		t.Fatalf("no next item may start, calls=%d", one.callCount())
	}
}

func TestP5CycleSelectedButUnclaimedNotInterrupted(t *testing.T) {
	one := &fakeOneShot{fn: func(_ int, ctx context.Context) (incrementalexec.Result, error) {
		<-ctx.Done()
		// Selected but ClaimWork never committed.
		return incrementalexec.Result{Selected: true}, ctx.Err()
	}}
	res, err := p5Runner(t, one).Run(context.Background(),
		incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("cycle wall deadline is a normal bounded stop, got %v", err)
	}
	if res.StopReason != incrementalexec.StopMaxWallTime {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if res.InterruptedInFlight {
		t.Fatal("selected-but-unclaimed must report InterruptedInFlight=false")
	}
}

func TestP5CycleParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	one := &fakeOneShot{fn: func(_ int, ctx context.Context) (incrementalexec.Result, error) {
		cancel()
		<-ctx.Done()
		return p5Selected(7), ctx.Err()
	}}
	res, err := p5Runner(t, one).Run(parent,
		incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: time.Minute})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want parent context.Canceled, got %v", err)
	}
	if res.StopReason != incrementalexec.StopContextCancelled {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if one.callCount() != 1 {
		t.Fatalf("no next item may start, calls=%d", one.callCount())
	}
}

func TestP5CycleParentCancelledBeforeFirstItem(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	one := &fakeOneShot{}
	res, err := p5Runner(t, one).Run(ctx,
		incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: time.Minute})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if res.StopReason != incrementalexec.StopContextCancelled {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if one.callCount() != 0 {
		t.Fatalf("no item may start, calls=%d", one.callCount())
	}
}
func TestP5CycleSerialOnly(t *testing.T) {
	var active, maxActive int32
	one := &fakeOneShot{fn: func(i int, _ context.Context) (incrementalexec.Result, error) {
		n := atomic.AddInt32(&active, 1)
		for {
			m := atomic.LoadInt32(&maxActive)
			if n <= m || atomic.CompareAndSwapInt32(&maxActive, m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		if i >= 4 {
			return incrementalexec.Result{}, incrementalexec.ErrNoEligibleWork
		}
		return p5Selected(int64(i + 1)), nil
	}}
	res, err := p5Runner(t, one).Run(context.Background(),
		incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != incrementalexec.StopNoEligibleWork {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if n := atomic.LoadInt32(&maxActive); n != 1 {
		t.Fatalf("cycle must be strictly serial, max concurrent ExecuteOne = %d", n)
	}
}
