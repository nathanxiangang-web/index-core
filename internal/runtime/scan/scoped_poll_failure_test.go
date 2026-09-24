package scan_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"

	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

const p1RootID = "f0000000-0000-0000-0000-0000000000e1"

func setupP1Root(t *testing.T, baseURL string) (*postgres.Store, string) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	if err := st.CreateRoot(ctx, st.Pool(), p1RootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, p1RootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	acfg, _ := json.Marshal(map[string]string{"base_url": baseURL, "path": "/"})
	if err := st.UpsertAdapterConfig(ctx, p1RootID, postgres.AdapterConfig{CollectorKind: "alist", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}
	return st, p1RootID
}

func p1PresentCount(t *testing.T, st *postgres.Store, rootID string) int {
	t.Helper()
	page, err := postgres.NewQueryReader(st.Pool()).ListActivePage(context.Background(), rootID, nil, 1000)
	if err != nil {
		t.Fatalf("Q6 list active: %v", err)
	}
	return len(page.Items)
}

// TestPollHarnessActuatorFailureFailsClosed proves a 403 refresh-permission
// failure and a provider/list error each surface exactly once through the P1
// harness (no tight retry) and never mutate Canonical state.
func TestPollHarnessActuatorFailureFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"403 refresh permission", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"code":403,"message":"Refresh without permission","data":null}`)
		}},
		{"provider error", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":500,"message":"boom","data":null}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			st, rootID := setupP1Root(t, srv.URL)
			svc := scan.New(st, "", "", 10*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

			calls := 0
			clk := &testClock{}
			h := newPollHarness(defaultPollLimits(), clk.now, func(ctx context.Context, scope string) (scan.Result, error) {
				calls++
				return svc.ScanScope(ctx, rootID, scope, 100)
			})
			h.addScope("/", cadenceHot)
			r := h.runCycle(context.Background())

			if r.attempted != 1 || len(r.results) != 1 {
				t.Fatalf("failing scope must be attempted exactly once per cycle, got attempted=%d results=%d",
					r.attempted, len(r.results))
			}
			if r.results[0].err == nil {
				t.Fatal("actuator failure must surface as an error")
			}
			if calls != 1 {
				t.Fatalf("no tight retry allowed, got %d calls", calls)
			}
			if n := p1PresentCount(t, st, rootID); n != 0 {
				t.Fatalf("failed observation must not mutate canonical state, got %d PRESENT", n)
			}
		})
	}
}

// TestPollHarnessOverMaxEntriesFailsClosed proves the existing P0 max-entry guard
// fails closed through the harness with no canonical mutation.
func TestPollHarnessOverMaxEntriesFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"content":[
			{"name":"a","is_dir":false,"size":1},
			{"name":"b","is_dir":false,"size":1}],"total":2}}`)
	}))
	defer srv.Close()
	st, rootID := setupP1Root(t, srv.URL)
	svc := scan.New(st, "", "", 10*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	lim := defaultPollLimits()
	lim.maxEntriesPerScope = 1
	clk := &testClock{}
	h := newPollHarness(lim, clk.now, func(ctx context.Context, scope string) (scan.Result, error) {
		return svc.ScanScope(ctx, rootID, scope, lim.maxEntriesPerScope)
	})
	h.addScope("/", cadenceHot)
	r := h.runCycle(context.Background())
	if len(r.results) != 1 || r.results[0].err == nil {
		t.Fatalf("over-max_entries must fail closed through the harness, got %+v", r.results)
	}
	if n := p1PresentCount(t, st, rootID); n != 0 {
		t.Fatalf("over-max_entries must not mutate canonical state, got %d PRESENT", n)
	}
}
