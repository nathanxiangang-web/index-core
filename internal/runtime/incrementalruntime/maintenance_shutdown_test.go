package incrementalruntime_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
)

type shutdownMaintenanceStore struct {
	fakeStore
	entered chan struct{}
	release chan struct{}
	result  error
}

func (s *shutdownMaintenanceStore) ListDueRetryWork(context.Context, time.Time, int) ([]state.DirtyScopeWork, error) {
	close(s.entered)
	<-s.release
	return nil, s.result
}

// Channels put cancellation INSIDE the Store call, not on a scheduler-dependent
// gap between cycles. A canceled parent alone must not hide an unrelated fault.
func TestIssue138MaintenanceShutdownClassification(t *testing.T) {
	realFailure := errors.New("synthetic-store-failure-do-not-log")
	for _, tc := range []struct {
		name         string
		cancelParent bool
		cause        error
		wantFatal    bool
	}{
		{"owned-wrapped-cancellation", true, fmt.Errorf("wrapped: %w", context.Canceled), false},
		{"real-error-during-shutdown", true, realFailure, true},
		{"cancellation-with-live-parent", false, context.Canceled, true},
		{"different-timeout-during-shutdown", true, context.DeadlineExceeded, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &shutdownMaintenanceStore{entered: make(chan struct{}), release: make(chan struct{}), result: tc.cause}
			cycle := &fakeCycle{}
			var log bytes.Buffer
			rt, err := incrementalruntime.New(store, cycle, p10Cfg(), slog.New(slog.NewJSONHandler(&log, nil)), nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			release := sync.OnceFunc(func() { close(store.release) })
			done := make(chan struct{})
			var runErr error
			go func() { runErr = rt.Run(ctx); close(done) }()
			t.Cleanup(func() {
				cancel()
				release()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("runtime actor did not join")
				}
			})
			select {
			case <-store.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("maintenance not entered")
			}
			if tc.cancelParent {
				cancel()
			}
			release()
			select {
			case <-done:
				err := runErr
				if (err != nil) != tc.wantFatal {
					t.Fatal("wrong shutdown classification", err)
				}
				if tc.wantFatal && !errors.Is(err, tc.cause) {
					t.Fatal("original failure chain lost", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("runtime did not terminate")
			}
			if cycle.callCount() != 0 {
				t.Fatal("cycle ran after failed/canceled maintenance")
			}
			if strings.Contains(log.String(), "incremental_runtime_fatal") != tc.wantFatal {
				t.Fatal("wrong fatal event", log.String())
			}
			if strings.Contains(log.String(), realFailure.Error()) {
				t.Fatal("raw Store error leaked")
			}
		})
	}
}
