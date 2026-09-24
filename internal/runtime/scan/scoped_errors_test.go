package scan_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

const p4ScopeRootID = "b2000000-0000-0000-0000-0000000000b1"

func p4ScanSetup(t *testing.T, adapterKind, baseURL, rootPath string, withPolicy bool, lifecycle domain.RootLifecycleState) (*postgres.Store, context.Context) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	if err := st.CreateRoot(ctx, st.Pool(), p4ScopeRootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if withPolicy {
		if err := st.UpsertRootPolicy(ctx, p4ScopeRootID, postgres.RootPolicy{
			RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
			MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
		}); err != nil {
			t.Fatalf("policy: %v", err)
		}
	}
	if adapterKind != "" {
		acfg, _ := json.Marshal(map[string]string{"base_url": baseURL, "path": rootPath})
		if err := st.UpsertAdapterConfig(ctx, p4ScopeRootID,
			postgres.AdapterConfig{CollectorKind: adapterKind, Config: acfg}); err != nil {
			t.Fatalf("adapter: %v", err)
		}
	}
	if lifecycle != domain.RootActive {
		if _, err := st.TransitionRootLifecycle(ctx, p4ScopeRootID, lifecycle); err != nil {
			t.Fatalf("transition root to %s: %v", lifecycle, err)
		}
	}
	return st, ctx
}

func p4ScanService(st *postgres.Store) *scan.Service {
	return scan.New(st, "", "", 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func assertScanScopeKind(t *testing.T, err error, want scan.ScopeFailureKind) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a typed scoped error, got nil")
	}
	var se *scan.ScopeError
	if !errors.As(err, &se) {
		t.Fatalf("error must be a typed *scan.ScopeError, got %T: %v", err, err)
	}
	if se.Kind != want {
		t.Fatalf("scope kind = %s, want %s (%v)", se.Kind, want, err)
	}
}

func p4CodeServer(t *testing.T, code *atomic.Int32, total string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		c := int(code.Load())
		switch c {
		case 200:
			_, _ = io.WriteString(w, `{"code":200,"message":"ok","data":{"content":[],"total":`+total+`}}`)
		default:
			_, _ = io.WriteString(w, `{"code":`+itoa(c)+`,"message":"x","data":null}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestScanScopeTypedProviderClassification(t *testing.T) {
	var code atomic.Int32
	code.Store(200)
	srv := p4CodeServer(t, &code, "0")
	st, ctx := p4ScanSetup(t, "alist", srv.URL, "/", true, domain.RootActive)
	svc := p4ScanService(st)

	for _, tc := range []struct {
		code int32
		want scan.ScopeFailureKind
	}{
		{401, scan.ScopeFailureAuthOrPermission},
		{403, scan.ScopeFailureAuthOrPermission},
		{429, scan.ScopeFailureThrottled},
		{500, scan.ScopeFailureTransientProvider},
		{503, scan.ScopeFailureTransientProvider},
	} {
		code.Store(tc.code)
		_, err := svc.ScanScope(ctx, p4ScopeRootID, "/", 100)
		assertScanScopeKind(t, err, tc.want)
	}
}

func TestScanScopeTypedOverflowIsTooLarge(t *testing.T) {
	var code atomic.Int32
	code.Store(200)
	// total=5 while max_entries=1 -> provider-declared overflow.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"message":"ok","data":{"content":[{"name":"a","size":1}],"total":5}}`)
	}))
	defer srv.Close()
	st, ctx := p4ScanSetup(t, "alist", srv.URL, "/", true, domain.RootActive)
	svc := p4ScanService(st)

	_, err := svc.ScanScope(ctx, p4ScopeRootID, "/", 1)
	assertScanScopeKind(t, err, scan.ScopeFailureTooLarge)
}

func TestScanScopeTypedInvalidScope(t *testing.T) {
	var code atomic.Int32
	code.Store(200)
	srv := p4CodeServer(t, &code, "0")
	st, ctx := p4ScanSetup(t, "alist", srv.URL, "/", true, domain.RootActive)
	svc := p4ScanService(st)

	_, err := svc.ScanScope(ctx, p4ScopeRootID, "../escape", 100)
	assertScanScopeKind(t, err, scan.ScopeFailureInvalidScope)

	// A non-root scope that is not a unique PRESENT canonical directory.
	_, err = svc.ScanScope(ctx, p4ScopeRootID, "/missing", 100)
	assertScanScopeKind(t, err, scan.ScopeFailureInvalidScope)
}

func TestScanScopeTypedConfigClassification(t *testing.T) {
	// Missing adapter binding.
	st, ctx := p4ScanSetup(t, "", "", "/", true, domain.RootActive)
	_, err := p4ScanService(st).ScanScope(ctx, p4ScopeRootID, "/", 100)
	assertScanScopeKind(t, err, scan.ScopeFailureConfigInvalid)

	// Unsupported collector kind.
	var code atomic.Int32
	code.Store(200)
	srv := p4CodeServer(t, &code, "0")
	st2, ctx2 := p4ScanSetup(t, "rclone", srv.URL, "/", true, domain.RootActive)
	_, err = p4ScanService(st2).ScanScope(ctx2, p4ScopeRootID, "/", 100)
	assertScanScopeKind(t, err, scan.ScopeFailureConfigInvalid)

	// Invalid max_entries bound.
	st3, ctx3 := p4ScanSetup(t, "alist", srv.URL, "/", true, domain.RootActive)
	for _, n := range []int{-1, scan.MaxScopedEntries + 1} {
		_, err = p4ScanService(st3).ScanScope(ctx3, p4ScopeRootID, "/", n)
		assertScanScopeKind(t, err, scan.ScopeFailureConfigInvalid)
	}

	// Missing root reconcile policy (provider succeeds, policy absent).
	st4, ctx4 := p4ScanSetup(t, "alist", srv.URL, "/", false, domain.RootActive)
	_, err = p4ScanService(st4).ScanScope(ctx4, p4ScopeRootID, "/", 100)
	assertScanScopeKind(t, err, scan.ScopeFailureConfigInvalid)
}

func TestScanScopeTypedDeletedRootIsRootInactive(t *testing.T) {
	var code atomic.Int32
	code.Store(200)
	srv := p4CodeServer(t, &code, "0")
	st, ctx := p4ScanSetup(t, "alist", srv.URL, "/", true, domain.RootDeleted)
	_, err := p4ScanService(st).ScanScope(ctx, p4ScopeRootID, "/", 100)
	assertScanScopeKind(t, err, scan.ScopeFailureRootInactive)
}
func TestScanScopeTypedRootPathIsConfigInvalid(t *testing.T) {
	var code atomic.Int32
	code.Store(200)
	srv := p4CodeServer(t, &code, "0")
	// A ".." component in the configured provider root path is adapter
	// configuration, not the persisted DirtyScopeWork scope.
	st, ctx := p4ScanSetup(t, "alist", srv.URL, "/../escape", true, domain.RootActive)
	_, err := p4ScanService(st).ScanScope(ctx, p4ScopeRootID, "/", 100)
	assertScanScopeKind(t, err, scan.ScopeFailureConfigInvalid)
}

func TestScanScopeTypedNullPayloadIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"message":"ok","data":null}`)
	}))
	defer srv.Close()
	st, ctx := p4ScanSetup(t, "alist", srv.URL, "/", true, domain.RootActive)
	_, err := p4ScanService(st).ScanScope(ctx, p4ScopeRootID, "/", 100)
	assertScanScopeKind(t, err, scan.ScopeFailureTransientProvider)
}

func TestScanScopeTypedDBFailureIsInternal(t *testing.T) {
	var code atomic.Int32
	code.Store(200)
	srv := p4CodeServer(t, &code, "0")
	st, ctx := p4ScanSetup(t, "alist", srv.URL, "/", true, domain.RootActive)
	svc := p4ScanService(st)

	// A storage failure must be INTERNAL, never a permanent CONFIG_INVALID or
	// INVALID_SCOPE classification.
	st.Pool().Close()
	_, err := svc.ScanScope(ctx, p4ScopeRootID, "/", 100)
	assertScanScopeKind(t, err, scan.ScopeFailureInternal)
}

func p4ScanSetupWithCreds(t *testing.T, baseURL string) (*postgres.Store, context.Context) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	if err := st.CreateRoot(ctx, st.Pool(), p4ScopeRootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, p4ScopeRootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	acfg, _ := json.Marshal(map[string]string{
		"base_url": baseURL, "path": "/",
		"username_env": "P4_SCAN_LOGIN_USER", "password_env": "P4_SCAN_LOGIN_PASS",
	})
	if err := st.UpsertAdapterConfig(ctx, p4ScopeRootID,
		postgres.AdapterConfig{CollectorKind: "alist", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}
	return st, ctx
}

func TestScanScopeTypedLoginPermissionIsAuth(t *testing.T) {
	t.Setenv("P4_SCAN_LOGIN_USER", "operator")
	t.Setenv("P4_SCAN_LOGIN_PASS", "secret")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/api/auth/login") {
			_, _ = io.WriteString(w, `{"code":403,"message":"forbidden","data":null}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":200,"message":"ok","data":{"content":[],"total":0}}`)
	}))
	defer srv.Close()

	st, ctx := p4ScanSetupWithCreds(t, srv.URL)
	_, err := p4ScanService(st).ScanScope(ctx, p4ScopeRootID, "/", 100)
	assertScanScopeKind(t, err, scan.ScopeFailureAuthOrPermission)
}
