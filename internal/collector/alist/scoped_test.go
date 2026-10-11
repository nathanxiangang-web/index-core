package alist_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/collector/adapter"
	"github.com/nathanxiangang-web/index-core/internal/collector/alist"
	"github.com/nathanxiangang-web/index-core/internal/domain"
)

func TestScanScopeExplicitEmptyResponseContract(t *testing.T) {
	cases := []struct {
		name string
		body string
		kind alist.ScopedErrorKind
	}{
		{"null zero", `{"code":200,"data":{"content":null,"total":0}}`, ""},
		{"array zero", `{"code":200,"data":{"content":[],"total":0}}`, ""},
		{"whitespace null", `{"code":200,"data":{"content": null ,"total":0}}`, ""},
		{"missing data", `{"code":200}`, alist.ScopedTransientProvider},
		{"null data", `{"code":200,"data":null}`, alist.ScopedTransientProvider},
		{"array data", `{"code":200,"data":[]}`, alist.ScopedTransientProvider},
		{"missing content zero", `{"code":200,"data":{"total":0}}`, alist.ScopedTransientProvider},
		{"missing total", `{"code":200,"data":{"content":null}}`, alist.ScopedTransientProvider},
		{"null total", `{"code":200,"data":{"content":null,"total":null}}`, alist.ScopedTransientProvider},
		{"string total", `{"code":200,"data":{"content":null,"total":"0"}}`, alist.ScopedTransientProvider},
		{"fraction total", `{"code":200,"data":{"content":null,"total":0.5}}`, alist.ScopedTransientProvider},
		{"decimal total", `{"code":200,"data":{"content":null,"total":0.0}}`, alist.ScopedTransientProvider},
		{"negative total", `{"code":200,"data":{"content":null,"total":-1}}`, alist.ScopedTransientProvider},
		{"null positive total", `{"code":200,"data":{"content":null,"total":1}}`, alist.ScopedTransientProvider},
		{"object content", `{"code":200,"data":{"content":{},"total":0}}`, alist.ScopedTransientProvider},
		{"string content", `{"code":200,"data":{"content":"null","total":0}}`, alist.ScopedTransientProvider},
		{"number content", `{"code":200,"data":{"content":0,"total":0}}`, alist.ScopedTransientProvider},
		{"bool content", `{"code":200,"data":{"content":false,"total":0}}`, alist.ScopedTransientProvider},
		{"truncated array", `{"code":200,"data":{"content":[],"total":1}}`, alist.ScopedTransientProvider},
		{"array count mismatch", `{"code":200,"data":{"content":[{"name":"a"}],"total":0}}`, alist.ScopedTransientProvider},
		{"overflow", `{"code":200,"data":{"content":[{"name":"a"},{"name":"b"}],"total":2}}`, alist.ScopedTooLarge},
		{"unauthorized", `{"code":401,"data":{"content":null,"total":0}}`, alist.ScopedAuthOrPermission},
		{"forbidden", `{"code":403,"data":{"content":null,"total":0}}`, alist.ScopedAuthOrPermission},
		{"provider failure", `{"code":500,"data":{"content":null,"total":0}}`, alist.ScopedTransientProvider},
		{"malformed JSON", `{"code":200,"data":{"content":null,"total":0}`, alist.ScopedTransientProvider},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var req scopedListReq
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if r.Method != http.MethodPost || r.URL.Path != "/api/fs/list" || !req.Refresh || req.Page != 1 || req.PerPage != 2 {
					t.Errorf("unexpected forced single-page request: %s %s %+v", r.Method, r.URL.Path, req)
				}
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			raw, err := (alist.Adapter{BaseURL: srv.URL}).ScanScope(context.Background(), "/empty", 1)
			if calls.Load() != 1 {
				t.Fatalf("calls=%d, want exactly one", calls.Load())
			}
			if tc.kind != "" {
				var typed *alist.ScopedError
				if !errors.As(err, &typed) || typed.Kind != tc.kind {
					t.Fatalf("error=%v, want typed %s", err, tc.kind)
				}
				if !reflect.DeepEqual(raw, adapter.RawScan{}) {
					t.Fatalf("invalid payload must return no observation/removal evidence: %+v", raw)
				}
				return
			}
			if err != nil || len(raw.Entries) != 0 || raw.TraversalStatus != domain.TraversalPartial || raw.Freshness != domain.FreshRefreshed || raw.Assurance != domain.WeakFailureVisibility || raw.ProviderIdentityAssurance != domain.IdentityUnverified || raw.SkippedKnownEmpty || raw.SkippedScopes != nil {
				t.Fatalf("explicit empty must retain additive-safe PARTIAL evidence: %+v, err=%v", raw, err)
			}
		})
	}
}

type scopedListReq struct {
	Path    string `json:"path"`
	Page    int    `json:"page"`
	PerPage int    `json:"per_page"`
	Refresh bool   `json:"refresh"`
}

// TestScanScopeSingleForcedRefresh proves the P0 single-response contract:
// exactly one refresh=true /api/fs/list request, page=1, per_page=maxEntries+1,
// direct children only, and the mandatory additive-safe PARTIAL semantics.
func TestScanScopeSingleForcedRefresh(t *testing.T) {
	var calls int32
	var got scopedListReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		// The child directory /downloads/newdir also has content, but P0 must
		// never request it (no recursion).
		io.WriteString(w, `{"code":200,"message":"success","data":{"content":[
			{"name":"a.txt","size":5,"is_dir":false,"modified":"2026-01-02T03:04:05Z","hash_info":{"sha1":"abc"}},
			{"name":"newdir","size":0,"is_dir":true,"modified":"2026-01-02T03:04:05Z"}],"total":2}}`)
	}))
	defer srv.Close()

	raw, err := alist.Adapter{BaseURL: srv.URL}.ScanScope(context.Background(), "/downloads", 100)
	if err != nil {
		t.Fatalf("ScanScope: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("exactly one request required, got %d", n)
	}
	if !got.Refresh {
		t.Fatal("request must set refresh=true")
	}
	if got.Page != 1 {
		t.Fatalf("request must use page=1, got %d", got.Page)
	}
	if got.PerPage != 101 {
		t.Fatalf("request must use per_page=maxEntries+1=101, got %d", got.PerPage)
	}
	if got.Path != "/downloads" {
		t.Fatalf("request path must be the scope, got %q", got.Path)
	}

	if len(raw.Entries) != 2 {
		t.Fatalf("direct children only: expected 2 entries, got %d", len(raw.Entries))
	}
	for _, e := range raw.Entries {
		if e.EntryLocalID == "/downloads/newdir/x" {
			t.Fatal("scan scope must not recurse into subdirectories")
		}
	}
	if raw.TraversalStatus != domain.TraversalPartial {
		t.Fatalf("scoped observation must be PARTIAL, got %s", raw.TraversalStatus)
	}
	if raw.Freshness != domain.FreshRefreshed {
		t.Fatalf("scoped observation must be FRESH_REFRESHED, got %s", raw.Freshness)
	}
	if raw.Assurance != domain.WeakFailureVisibility {
		t.Fatalf("scoped observation must be WEAK_FAILURE_VISIBILITY, got %s", raw.Assurance)
	}
	if raw.ProviderIdentityAssurance != domain.IdentityUnverified {
		t.Fatalf("scoped observation must be UNVERIFIED, got %s", raw.ProviderIdentityAssurance)
	}
	if raw.SkippedScopes != nil || raw.SkippedKnownEmpty {
		t.Fatal("scoped skip evidence must stay UNKNOWN (never confirmed-empty)")
	}
}

func TestScanScopeEmptyIsLegalPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":200,"message":"success","data":{"content":[],"total":0}}`)
	}))
	defer srv.Close()

	raw, err := alist.Adapter{BaseURL: srv.URL}.ScanScope(context.Background(), "/empty", 10)
	if err != nil {
		t.Fatalf("empty scope must be a legal PARTIAL observation, got %v", err)
	}
	if len(raw.Entries) != 0 {
		t.Fatalf("empty scope must yield zero entries, got %d", len(raw.Entries))
	}
	if raw.TraversalStatus != domain.TraversalPartial {
		t.Fatalf("empty scope must still be PARTIAL, got %s", raw.TraversalStatus)
	}
}

func TestScanScopeOverflowFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":200,"message":"success","data":{"content":[
			{"name":"a","is_dir":false,"size":1},
			{"name":"b","is_dir":false,"size":1}],"total":2}}`)
	}))
	defer srv.Close()

	if _, err := (alist.Adapter{BaseURL: srv.URL}).ScanScope(context.Background(), "/big", 1); err == nil {
		t.Fatal("total > max_entries must fail closed")
	}
}

func TestScanScopeTotalCountMismatchFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Declares 3 but returns 2: a truncated/incoherent response.
		io.WriteString(w, `{"code":200,"message":"success","data":{"content":[
			{"name":"a","is_dir":false,"size":1},
			{"name":"b","is_dir":false,"size":1}],"total":3}}`)
	}))
	defer srv.Close()

	if _, err := (alist.Adapter{BaseURL: srv.URL}).ScanScope(context.Background(), "/x", 100); err == nil {
		t.Fatal("total != len(content) must fail closed")
	}
}

func TestScanScopeRefreshPermission403Surfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"code":403,"message":"Refresh without permission","data":null}`)
	}))
	defer srv.Close()

	if _, err := (alist.Adapter{BaseURL: srv.URL}).ScanScope(context.Background(), "/x", 10); err == nil {
		t.Fatal("403 Refresh without permission must surface as an error")
	}
}

func TestScanScopeProviderErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":500,"message":"boom","data":null}`)
	}))
	defer srv.Close()

	if _, err := (alist.Adapter{BaseURL: srv.URL}).ScanScope(context.Background(), "/x", 10); err == nil {
		t.Fatal("provider list error must surface as an error")
	}
}

// TestScanScopeRejectsOverHardCap proves the P0 hard cap: a caller can never
// widen a scoped observation, and an overflow-sized bound (math.MaxInt) is
// rejected instead of becoming a giant per_page.
func TestScanScopeRejectsOverHardCap(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		io.WriteString(w, `{"code":200,"message":"success","data":{"content":[],"total":0}}`)
	}))
	defer srv.Close()

	a := alist.Adapter{BaseURL: srv.URL}
	for _, n := range []int{-1, alist.MaxScopedEntries + 1, math.MaxInt} {
		if _, err := a.ScanScope(context.Background(), "/x", n); err == nil {
			t.Fatalf("max_entries=%d must be rejected by the P0 hard cap", n)
		}
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("rejected max_entries must not reach the provider, got %d requests", n)
	}
}

// TestScanScopeAllowsExactHardCap proves the inclusive boundary: maxEntries ==
// MaxScopedEntries is allowed, and the request uses per_page == MaxScopedEntries+1.
func TestScanScopeAllowsExactHardCap(t *testing.T) {
	var got scopedListReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":200,"message":"success","data":{"content":[
			{"name":"a","is_dir":false,"size":1}],"total":1}}`)
	}))
	defer srv.Close()

	raw, err := (alist.Adapter{BaseURL: srv.URL}).ScanScope(context.Background(), "/x", alist.MaxScopedEntries)
	if err != nil {
		t.Fatalf("maxEntries == MaxScopedEntries must be allowed, got %v", err)
	}
	if got.PerPage != alist.MaxScopedEntries+1 {
		t.Fatalf("per_page must be MaxScopedEntries+1=%d, got %d", alist.MaxScopedEntries+1, got.PerPage)
	}
	if len(raw.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(raw.Entries))
	}
}
