package alist_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/collector/alist"
	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// G3-R2.7: the adapter must paginate until all `total` entries are collected.
func TestListPaginationCollectsAllPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Page    int `json:"page"`
			PerPage int `json:"per_page"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Page {
		case 1:
			io.WriteString(w, `{"code":200,"message":"success","data":{"total":2,"content":[
				{"name":"a.txt","size":1,"is_dir":false,"modified":"2026-01-02T03:04:05Z"}]}}`)
		case 2:
			io.WriteString(w, `{"code":200,"message":"success","data":{"total":2,"content":[
				{"name":"b.txt","size":2,"is_dir":false,"modified":"2026-01-02T03:04:05Z"}]}}`)
		default:
			io.WriteString(w, `{"code":200,"message":"success","data":{"total":2,"content":[]}}`)
		}
	}))
	defer srv.Close()

	raw, err := alist.Adapter{BaseURL: srv.URL}.Scan(context.Background(), "/")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if raw.TraversalStatus != domain.TraversalSuccess {
		t.Fatalf("paged scan must be SUCCESS, got %s (%s)", raw.TraversalStatus, raw.ErrorSummary)
	}
	if len(raw.Entries) != 2 {
		t.Fatalf("pagination must collect both pages, got %d entries", len(raw.Entries))
	}
}

// G3-R2.7: a truncated listing (total > collected) must fail closed, not be
// silently classified SUCCESS.
func TestListTruncationFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Page int `json:"page"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Page == 1 {
			io.WriteString(w, `{"code":200,"message":"success","data":{"total":5,"content":[
				{"name":"a.txt","size":1,"is_dir":false,"modified":"2026-01-02T03:04:05Z"}]}}`)
			return
		}
		io.WriteString(w, `{"code":200,"message":"success","data":{"total":5,"content":[]}}`)
	}))
	defer srv.Close()

	raw, err := alist.Adapter{BaseURL: srv.URL}.Scan(context.Background(), "/")
	if err != nil {
		t.Fatalf("scan transport error surfaced via RawScan, got %v", err)
	}
	if raw.TraversalStatus != domain.TraversalFailed {
		t.Fatalf("truncated listing must be FAILED, got %s", raw.TraversalStatus)
	}
	if len(raw.ErrorSummary) == 0 {
		t.Fatal("truncation must carry an error summary")
	}
	_ = fmt.Sprintf("%d", len(raw.Entries))
}
