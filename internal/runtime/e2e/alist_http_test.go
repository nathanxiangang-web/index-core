package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"

	"os"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/runtime/worker"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/internal/transport/httpapi"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// G3-R1: real AList/OpenList source -> Collector Adapter -> Snapshot ->
// Coordinator -> Canonical -> HTTP /v1. Opt-in via INDEXCORE_ALIST_URL.
func TestAlphaRuntimeWithRealAListSource(t *testing.T) {
	base := os.Getenv("INDEXCORE_ALIST_URL")
	if base == "" {
		t.Skip("set INDEXCORE_ALIST_URL (+ USER/PASS/PATH) to run against a real AList/OpenList instance")
	}
	path := os.Getenv("INDEXCORE_ALIST_PATH")
	if path == "" {
		path = "/"
	}
	pool := testutil.Pool(t)
	ctx := context.Background()
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	const rootID = "e7000000-0000-0000-0000-000000000001"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	// Credentials are persisted only as environment-variable REFERENCES; the
	// secret values are resolved at runtime and never stored (G3-R2.8).
	acfg, _ := json.Marshal(map[string]string{
		"base_url": base, "path": path,
		"username_env": "INDEXCORE_ALIST_USER", "password_env": "INDEXCORE_ALIST_PASS",
	})
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "alist", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := httpapi.New(httpapi.Deps{
		Query: postgres.NewQueryReader(pool), Readiness: postgres.NewReadiness(pool), Logger: logger, Version: "test",
	})
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	httpBase := "http://" + ln.Addr().String()

	wk := worker.NewWithIntervals(st, 4, 50*time.Millisecond, 50*time.Millisecond, logger)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = wk.Run(wctx) }()

	svc := scan.New(st, "rclone", "", 60*time.Second, logger)
	res, err := svc.Scan(ctx, rootID)
	if err != nil {
		t.Fatalf("scan real AList: %v", err)
	}
	if res.Outcome.Status != domain.AdmissionApplied {
		t.Fatalf("real AList scan must APPLY, got %s", res.Outcome.Status)
	}
	items := httpActive(t, httpBase, rootID)
	if len(items) == 0 {
		t.Fatal("HTTP /v1 must expose the AList-sourced resources")
	}
	// Additive-safe: a repeated identical AList scan is idempotent (NOOP).
	if res2, err := svc.Scan(ctx, rootID); err != nil || res2.Outcome.Status != domain.AdmissionNoop {
		t.Fatalf("repeat AList scan must be NOOP, got %+v err=%v", res2.Outcome, err)
	}
	t.Logf("real AList source produced %d HTTP-visible resources", len(items))
}
