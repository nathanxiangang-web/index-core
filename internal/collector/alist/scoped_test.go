package alist_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/collector/alist"
	"github.com/nathanxiangang-web/index-core/internal/domain"
)

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
