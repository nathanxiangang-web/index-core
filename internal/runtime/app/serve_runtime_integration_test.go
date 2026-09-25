package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// newP10AListHandler serves a root listing that contains /a.txt and counts forced
// refresh requests.
func newP10AListHandler(refresh *int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path    string `json:"path"`
			Refresh bool   `json:"refresh"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Refresh {
			atomic.AddInt32(refresh, 1)
		}
		content := []map[string]any{}
		if req.Path == "/" {
			content = []map[string]any{{
				"name": "a.txt", "size": 5, "is_dir": false,
				"modified": "2026-01-02T03:04:05Z", "hash_info": map[string]string{"sha1": "aaa"},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "message": "success",
			"data": map[string]any{"content": content, "total": len(content)},
		})
	}
}

func p10SeedProviderRoot(t *testing.T, st *postgres.Store, ctx context.Context, rootID, baseURL string) {
	t.Helper()
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root %s: %v", rootID, err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy %s: %v", rootID, err)
	}
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{
		CollectorKind: "alist", Config: p10AdapterConfig(baseURL),
	}); err != nil {
		t.Fatalf("adapter %s: %v", rootID, err)
	}
}

func p10WaitWorkState(t *testing.T, st *postgres.Store, ctx context.Context, rootID, scope string, want state.WorkState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if w, err := st.GetWork(ctx, rootID, scope); err == nil && w.WorkState == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	w, err := st.GetWork(ctx, rootID, scope)
	t.Fatalf("work %s/%s did not reach %s (got %+v err=%v)", rootID, scope, want, w.WorkState, err)
}

func p10AssertCanonicalPresent(t *testing.T, st *postgres.Store, rootID, path string) {
	t.Helper()
	var n int
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM index_canonical_resource
		  WHERE root_id=$1::uuid AND canonical_path=$2 AND resource_presence='PRESENT'`,
		rootID, path).Scan(&n); err != nil {
		t.Fatalf("canonical count %s: %v", path, err)
	}
	if n != 1 {
		t.Fatalf("expected one PRESENT canonical resource at %s, got %d", path, n)
	}
}

func p10SeedRetryWait(t *testing.T, st *postgres.Store, ctx context.Context, rootID, scope string, class state.ErrorClass, retryAt time.Time) {
	t.Helper()
	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: scope, Source: state.SourceManualOperator, Reason: state.ReasonManualVerify,
		Priority: state.PriorityNormal, SeenAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("merge %s: %v", rootID, err)
	}
	w, err := st.GetWork(ctx, rootID, scope)
	if err != nil {
		t.Fatalf("get work %s: %v", rootID, err)
	}
	claimed, err := st.ClaimWork(ctx, rootID, scope, w.Version, time.Now().UTC())
	if err != nil {
		t.Fatalf("claim %s: %v", rootID, err)
	}
	if _, err := st.CompleteFailure(ctx, rootID, scope, *claimed.ClaimedSignalSeq, class, &retryAt, time.Now().UTC()); err != nil {
		t.Fatalf("complete failure %s: %v", rootID, err)
	}
}

// TestP10IntegrationDueWatchExecutes proves the new daemon wiring reaches the
// accepted P6 -> P5/P4 -> P0 provider path and durably completes the work.
func TestP10IntegrationDueWatchExecutes(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()

	var refresh int32
	srv := httptest.NewServer(newP10AListHandler(&refresh))
	defer srv.Close()

	rootID := "b1000000-0000-0000-0000-0000000000b1"
	p10SeedProviderRoot(t, st, ctx, rootID, srv.URL)
	p10SeedWatch(t, st, ctx, rootID, "/", time.Now().UTC().Add(-time.Hour))

	cfg := p10Cfg("", true) // runtime enabled, Hint disabled
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(gctx, cfg, p9Logger(), pool, st, p9BlockingWorker) }()

	p10WaitWorkState(t, st, ctx, rootID, "/", state.WorkVerified, 10*time.Second)
	if got := atomic.LoadInt32(&refresh); got < 1 {
		t.Fatalf("P10 due-watch wake must reach the provider, refresh=%d", got)
	}
	p10AssertCanonicalPresent(t, st, rootID, "/a.txt")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve shutdown: %v", err)
	}
}

// TestP10IntegrationHintPostExecutesBeforeLongTimer proves HTTP 202 returns
// without waiting for execution, then the Hint wake drives the real P6/P4/P0 path
// well before the long scheduler timer.
func TestP10IntegrationHintPostExecutesBeforeLongTimer(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()

	var refresh int32
	release := make(chan struct{})
	var blockOnce int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path    string `json:"path"`
			Refresh bool   `json:"refresh"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Refresh {
			atomic.AddInt32(&refresh, 1)
		}
		// Block the first provider refresh so we can prove 202 does not wait.
		if atomic.CompareAndSwapInt32(&blockOnce, 0, 1) {
			<-release
		}
		content := []map[string]any{}
		if req.Path == "/" {
			content = []map[string]any{{
				"name": "a.txt", "size": 5, "is_dir": false,
				"modified": "2026-01-02T03:04:05Z", "hash_info": map[string]string{"sha1": "aaa"},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "message": "success",
			"data": map[string]any{"content": content, "total": len(content)},
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()

	rootID := "b1000000-0000-0000-0000-0000000000b2"
	p10SeedProviderRoot(t, st, ctx, rootID, srv.URL)

	cfg := p10Cfg(p9FreeAddr(t), true)
	cfg.IncrementalWakeInterval = 60 * time.Second // only the Hint wake can drive P10
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(gctx, cfg, p9Logger(), pool, st, p9BlockingWorker) }()
	p9WaitHint(t, cfg.HintAddr)

	start := time.Now()
	code, body := p9PostHint(cfg.HintAddr, p9Token,
		[]byte(`{"root_id":"`+rootID+`","scope_key":"/","reason":"POSSIBLE_CHANGE"}`))
	elapsed := time.Since(start)
	if code != 202 {
		t.Fatalf("hint POST status = %d (%s)", code, body)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("202 must not wait for provider execution, took %s", elapsed)
	}

	// The provider refresh is now blocked inside the P10-driven execution; the
	// Hint wake must complete it well before the 60s timer.
	deadline := time.Now().Add(10 * time.Second)
	for atomic.LoadInt32(&blockOnce) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	close(release)

	p10WaitWorkState(t, st, ctx, rootID, "/", state.WorkVerified, 10*time.Second)
	if got := atomic.LoadInt32(&refresh); got < 1 {
		t.Fatalf("Hint wake must reach the provider, refresh=%d", got)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve shutdown: %v", err)
	}
}

// TestP10IntegrationRetryPromotionExecutes proves a due allowed RETRY_WAIT row is
// promoted and executed in the same runtime pass, while INTERNAL stays RETRY_WAIT.
func TestP10IntegrationRetryPromotionExecutes(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()

	var refresh int32
	srv := httptest.NewServer(newP10AListHandler(&refresh))
	defer srv.Close()

	rootTransient := "b1000000-0000-0000-0000-0000000000b3"
	rootInternal := "b1000000-0000-0000-0000-0000000000b4"
	p10SeedProviderRoot(t, st, ctx, rootTransient, srv.URL)
	p10SeedProviderRoot(t, st, ctx, rootInternal, srv.URL)

	retryAt := time.Now().UTC().Add(200 * time.Millisecond)
	p10SeedRetryWait(t, st, ctx, rootTransient, "/", state.ErrorTransientProvider, retryAt)
	p10SeedRetryWait(t, st, ctx, rootInternal, "/", state.ErrorInternal, retryAt)

	cfg := p10Cfg("", true)
	cfg.IncrementalWakeInterval = time.Second
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(gctx, cfg, p9Logger(), pool, st, p9BlockingWorker) }()

	p10WaitWorkState(t, st, ctx, rootTransient, "/", state.WorkVerified, 15*time.Second)
	if got := atomic.LoadInt32(&refresh); got < 1 {
		t.Fatalf("promoted RETRY_WAIT must execute through the real provider, refresh=%d", got)
	}

	internalWork, err := st.GetWork(ctx, rootInternal, "/")
	if err != nil {
		t.Fatal(err)
	}
	if internalWork.WorkState != state.WorkRetryWait {
		t.Fatalf("INTERNAL must remain RETRY_WAIT, got %s", internalWork.WorkState)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve shutdown: %v", err)
	}
}
