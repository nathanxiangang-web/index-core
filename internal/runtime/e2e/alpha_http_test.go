package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/runtime/worker"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/internal/transport/httpapi"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// G3-R9: full Alpha runtime scenario through the REAL HTTP transport, not direct
// Store/QueryReader calls for the HTTP step.
func TestAlphaRuntimeOverHTTP(t *testing.T) {
	rcloneBin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone binary not installed")
	}
	pool := testutil.Pool(t)
	ctx := context.Background()
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	const rootID = "e6000000-0000-0000-0000-000000000001"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	dir := t.TempDir()
	writeF(t, filepath.Join(dir, "hello.txt"), "hello")
	acfg, _ := json.Marshal(map[string]string{"remote": "", "path": dir})
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "rclone", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}

	// Start the REAL HTTP transport.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := httpapi.New(httpapi.Deps{
		Query:     postgres.NewQueryReader(pool),
		Readiness: postgres.NewReadiness(pool),
		Logger:    logger,
		Version:   "test",
	})
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	base := "http://" + ln.Addr().String()

	wk := worker.NewWithIntervals(st, 4, 50*time.Millisecond, 50*time.Millisecond, logger)
	wctx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	go func() { _ = wk.Run(wctx) }()

	svc := scan.New(st, rcloneBin, "", 30*time.Second, logger)
	if _, err := svc.Scan(ctx, rootID); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Assert the HTTP step over a real HTTP client.
	items := httpActive(t, base, rootID)
	if len(items) < 1 {
		t.Fatalf("HTTP /v1 must return resources, got %d", len(items))
	}

	// Idempotent repeat.
	if res, err := svc.Scan(ctx, rootID); err != nil || res.Outcome.Status != domain.AdmissionNoop {
		t.Fatalf("repeat scan must be NOOP, got %+v err=%v", res.Outcome, err)
	}

	// Additive delta visible over HTTP.
	writeF(t, filepath.Join(dir, "added.txt"), "added")
	if res, err := svc.Scan(ctx, rootID); err != nil || res.Outcome.Status != domain.AdmissionApplied {
		t.Fatalf("delta scan must APPLY, got %+v err=%v", res.Outcome, err)
	}
	found := false
	for _, it := range httpActive(t, base, rootID) {
		if it == "/added.txt" {
			found = true
		}
	}
	if !found {
		t.Fatal("HTTP /v1 must expose the newly added resource")
	}

	// Restart the worker (daemon restart) and confirm readiness stays OK.
	cancelWorker()
	wk2 := worker.NewWithIntervals(st, 4, 50*time.Millisecond, 50*time.Millisecond, logger)
	wctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	go func() { _ = wk2.Run(wctx2) }()
	if code := httpStatus(t, base+"/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz must be 200 after restart, got %d", code)
	}
}

func httpActive(t *testing.T, base, rootID string) []string {
	t.Helper()
	resp, err := http.Get(base + "/v1/roots/" + rootID + "/active?limit=100")
	if err != nil {
		t.Fatalf("http get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("http active status %d", resp.StatusCode)
	}
	var body struct {
		Items []struct {
			CanonicalPath *string `json:"canonical_path"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := make([]string, 0, len(body.Items))
	for _, it := range body.Items {
		if it.CanonicalPath != nil {
			out = append(out, *it.CanonicalPath)
		}
	}
	return out
}

func httpStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("http get %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func writeF(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
