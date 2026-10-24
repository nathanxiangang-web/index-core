package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/internal/transport/hintapi"
)

// Observe the listener owned by runServe, not a port reserved and released by
// the test before runServe starts. No production listener hook is necessary.
type p9ObservedHint struct {
	hintTransport
	bound chan<- string
}

func (s p9ObservedHint) Serve(ln net.Listener) error {
	s.bound <- ln.Addr().String()
	return s.hintTransport.Serve(ln)
}

type p9HintRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error // read only after done closes
}

func (r *p9HintRun) wait() error {
	select {
	case <-r.done:
		return r.err
	case <-time.After(5 * time.Second):
		return errors.New("runServe did not finish shutdown")
	}
}

func p9AwaitBound(bound <-chan string, run *p9HintRun) (string, error) {
	exitError := func() error {
		if run.err == nil {
			return errors.New("runServe exited before Hint readiness without an error")
		}
		return fmt.Errorf("runServe exited before Hint readiness: %w", run.err)
	}
	select {
	case <-run.done:
		return "", exitError()
	case addr := <-bound:
		// Both signals may already be ready, e.g. immediate transport failure.
		select {
		case <-run.done:
			return "", exitError()
		default:
			return addr, nil
		}
	case <-time.After(3 * time.Second):
		return "", errors.New("Hint listener did not bind within 3s")
	}
}

func p9LaunchObservedHint(t *testing.T, pool *pgxpool.Pool, cfg config.Config) (*p9HintRun, string, error) {
	t.Helper()
	bound := make(chan string, 1)
	previous := newHintTransport
	newHintTransport = func(deps hintapi.Deps) (hintTransport, error) {
		inner, err := previous(deps)
		if err != nil {
			return nil, err
		}
		return p9ObservedHint{hintTransport: inner, bound: bound}, nil
	}
	t.Cleanup(func() { newHintTransport = previous })
	ctx, cancel := context.WithCancel(context.Background())
	run := &p9HintRun{cancel: cancel, done: make(chan struct{})}
	go func() {
		run.err = runServe(ctx, cfg, p9Logger(), pool, postgres.New(pool), p9BlockingWorker)
		close(run.done)
	}()
	// Join before restoring the injected transport or closing the database pool,
	// even when a readiness assertion fails. The close also publishes run.err.
	t.Cleanup(func() {
		cancel()
		if err := run.wait(); err != nil && !errors.Is(err, context.Canceled) {
			// Startup errors are returned to the caller for an exact assertion.
			select {
			case <-run.done:
			default:
				t.Error(err)
			}
		}
	})
	addr, err := p9AwaitBound(bound, run)
	return run, addr, err
}

func TestIssue138HintReadinessUsesOwnedEphemeralListener(t *testing.T) {
	pool := p9Schema(t)
	run, addr, err := p9LaunchObservedHint(t, pool, p9Cfg("127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "0" || port == "" {
		t.Fatalf("did not obtain the actual bound endpoint: %q (%v)", addr, err)
	}
	// A real request checks that observing a bound socket is not confused with
	// a successful Hint response. Authentication must remain fail-closed.
	if code, _ := p9PostHint(addr, "wrong", p9ValidHintBody()); code != 401 {
		t.Fatalf("unauthenticated request status = %d", code)
	}
	run.cancel()
	if err := run.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestIssue138HintReadinessPreservesStartupCause(t *testing.T) {
	pool := p9Schema(t)
	// Occupy the Hint endpoint until runServe exits: no released-port race in
	// this negative test either. This demonstrates precise error propagation,
	// NOT the root cause of the historical Actions failure.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg := p9Cfg(ln.Addr().String())
	run, _, err := p9LaunchObservedHint(t, pool, cfg)
	if err == nil {
		t.Fatal("colliding listeners must not report readiness")
	}
	if !errors.Is(err, run.wait()) {
		t.Fatalf("readiness lost the actual runServe error: %v", err)
	}
	if !strings.Contains(err.Error(), "bind hint listener") {
		t.Fatalf("expected precise startup failure instead of timeout: %v", err)
	}
	lock, err := postgres.New(pool).AcquireWriterLock(context.Background())
	if err != nil {
		t.Fatalf("startup failure leaked writer ownership: %v", err)
	}
	lock.Release(context.Background())
}

func TestIssue138HintReadinessRejectsExitedTransport(t *testing.T) {
	cause := errors.New("injected immediate transport failure")
	run := &p9HintRun{done: make(chan struct{}), err: cause}
	close(run.done)
	// Select order must not turn simultaneous bind/exit into false readiness.
	for i := 0; i < 100; i++ {
		bound := make(chan string, 1)
		bound <- "127.0.0.1:12345"
		if _, err := p9AwaitBound(bound, run); !errors.Is(err, cause) {
			t.Fatalf("iteration %d lost exit cause: %v", i, err)
		}
	}
}

func TestIssue138HintReadinessRejectsCleanEarlyExit(t *testing.T) {
	run := &p9HintRun{done: make(chan struct{})}
	close(run.done)
	if _, err := p9AwaitBound(make(chan string), run); err == nil {
		t.Fatal("an early clean exit is not a running Hint listener")
	}
}
