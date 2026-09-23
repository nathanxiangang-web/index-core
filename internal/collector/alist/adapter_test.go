package alist_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/collector/alist"
	"github.com/nathanxiangang-web/index-core/internal/domain"
)

func TestAdapterScanNormalizesAndRecurses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Path {
		case "/":
			io.WriteString(w, `{"code":200,"message":"success","data":{"content":[
				{"name":"a.txt","size":5,"is_dir":false,"modified":"2026-01-02T03:04:05Z","hash_info":{"sha1":"abc"}},
				{"name":"sub","size":0,"is_dir":true,"modified":"2026-01-02T03:04:05Z"}],"total":2}}`)
		case "/sub":
			io.WriteString(w, `{"code":200,"message":"success","data":{"content":[
				{"name":"b.txt","size":6,"is_dir":false,"modified":"2026-01-02T03:04:05Z","hash_info":{"md5":"def"}}],"total":1}}`)
		default:
			io.WriteString(w, `{"code":500,"message":"not found","data":null}`)
		}
	}))
	defer srv.Close()

	raw, err := alist.Adapter{BaseURL: srv.URL}.Scan(context.Background(), "/")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if raw.TraversalStatus != domain.TraversalSuccess {
		t.Fatalf("valid AList scan must be SUCCESS, got %s", raw.TraversalStatus)
	}
	if len(raw.Entries) != 3 {
		t.Fatalf("expected 3 entries (a.txt, sub, sub/b.txt), got %d", len(raw.Entries))
	}
	// Additive-safe: no structured skipped-set.
	if raw.SkippedKnownEmpty || raw.SkippedScopes != nil {
		t.Fatal("AList skip evidence must stay UNKNOWN (never confirmed-empty)")
	}
	if raw.Assurance != domain.WeakFailureVisibility {
		t.Fatalf("AList failure visibility must default to WEAK, got %s", raw.Assurance)
	}
	var nested bool
	for _, e := range raw.Entries {
		if e.EntryLocalID == "/sub/b.txt" {
			nested = true
			if e.ContentHash == nil || e.HashAlgorithm == nil || *e.HashAlgorithm != "md5" {
				t.Fatalf("nested hash must be normalized with algorithm, got %+v", e)
			}
		}
	}
	if !nested {
		t.Fatal("recursion must include nested /sub/b.txt")
	}
}

func TestAdapterScanFailureIsHonest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"code":500,"message":"boom","data":null}`)
	}))
	defer srv.Close()
	raw, err := alist.Adapter{BaseURL: srv.URL}.Scan(context.Background(), "/")
	if err != nil {
		t.Fatalf("transport errors surface via RawScan, got %v", err)
	}
	if raw.TraversalStatus != domain.TraversalFailed || len(raw.ErrorSummary) == 0 {
		t.Fatalf("a failing AList list must be FAILED with an error summary, got %+v", raw)
	}
	if raw.SkippedKnownEmpty {
		t.Fatal("a failed scan must not claim confirmed-empty skips")
	}
}
