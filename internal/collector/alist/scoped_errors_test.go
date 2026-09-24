package alist_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/collector/alist"
)

func assertScopedKind(t *testing.T, err error, want alist.ScopedErrorKind) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a typed scoped error, got nil")
	}
	var se *alist.ScopedError
	if !errors.As(err, &se) {
		t.Fatalf("error must be a typed *alist.ScopedError, got %T: %v", err, err)
	}
	if se.Kind != want {
		t.Fatalf("scoped kind = %s, want %s (%v)", se.Kind, want, err)
	}
}

func scopedCodeServer(t *testing.T, code int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":`+strconv.Itoa(code)+`,"message":"x","data":null}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestScopedErrorProviderClassification(t *testing.T) {
	cases := []struct {
		code int
		want alist.ScopedErrorKind
	}{
		{401, alist.ScopedAuthOrPermission},
		{403, alist.ScopedAuthOrPermission},
		{429, alist.ScopedThrottled},
		{500, alist.ScopedTransientProvider},
		{503, alist.ScopedTransientProvider},
		{404, alist.ScopedTransientProvider},
	}
	for _, tc := range cases {
		srv := scopedCodeServer(t, tc.code)
		_, err := (alist.Adapter{BaseURL: srv.URL}).ScanScope(context.Background(), "/x", 10)
		assertScopedKind(t, err, tc.want)
	}
}

func TestScopedErrorOverflowIsTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"message":"ok","data":{"content":[{"name":"a","size":1},{"name":"b","size":1}],"total":2}}`)
	}))
	defer srv.Close()

	_, err := (alist.Adapter{BaseURL: srv.URL}).ScanScope(context.Background(), "/x", 1)
	assertScopedKind(t, err, alist.ScopedTooLarge)
}

func TestScopedErrorMismatchIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"message":"ok","data":{"content":[{"name":"a","size":1}],"total":3}}`)
	}))
	defer srv.Close()

	_, err := (alist.Adapter{BaseURL: srv.URL}).ScanScope(context.Background(), "/x", 10)
	assertScopedKind(t, err, alist.ScopedTransientProvider)
}

func TestScopedErrorTransportIsTransient(t *testing.T) {
	_, err := (alist.Adapter{BaseURL: "http://127.0.0.1:1"}).ScanScope(context.Background(), "/x", 10)
	assertScopedKind(t, err, alist.ScopedTransientProvider)
}

func TestScopedErrorInvalidMaxEntriesIsConfigInvalid(t *testing.T) {
	srv := scopedCodeServer(t, 200)
	for _, n := range []int{-1, alist.MaxScopedEntries + 1} {
		_, err := (alist.Adapter{BaseURL: srv.URL}).ScanScope(context.Background(), "/x", n)
		assertScopedKind(t, err, alist.ScopedConfigInvalid)
	}
}
func TestScopedErrorEmptyBaseURLIsConfigInvalid(t *testing.T) {
	_, err := (alist.Adapter{}).ScanScope(context.Background(), "/x", 10)
	assertScopedKind(t, err, alist.ScopedConfigInvalid)
}

func TestScopedErrorInvalidBaseURLIsConfigInvalid(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer srv.Close()

	for _, raw := range []string{"://bad", "ftp://example.com", "/relative-only", "http://", "   "} {
		_, err := (alist.Adapter{BaseURL: raw}).ScanScope(context.Background(), "/x", 10)
		assertScopedKind(t, err, alist.ScopedConfigInvalid)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("an invalid base URL must not reach any provider, got %d requests", n)
	}
}

func TestScopedErrorIncompleteListPayloadIsTransient(t *testing.T) {
	// A scoped observation is only valid when the provider explicitly returns
	// both content and total; missing/null fields must never be completed from Go
	// zero values into a fabricated empty directory.
	bodies := []string{
		`{"code":200,"message":"ok","data":null}`,
		`{"code":200,"message":"ok","data":{}}`,
		`{"code":200,"message":"ok","data":{"total":0}}`,
		`{"code":200,"message":"ok","data":{"content":null,"total":0}}`,
		`{"code":200,"message":"ok","data":{"content":[]}}`,
		`{"code":200,"message":"ok","data":{"content":[],"total":-1}}`,
	}
	for _, body := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}))
		_, err := (alist.Adapter{BaseURL: srv.URL}).ScanScope(context.Background(), "/x", 10)
		assertScopedKind(t, err, alist.ScopedTransientProvider)
		srv.Close()
	}
}

func TestScopedErrorExplicitEmptyDirectoryIsValid(t *testing.T) {
	// content:[] with an explicit total:0 is a legal empty directory.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"message":"ok","data":{"content":[],"total":0}}`)
	}))
	defer srv.Close()

	raw, err := (alist.Adapter{BaseURL: srv.URL}).ScanScope(context.Background(), "/x", 10)
	if err != nil {
		t.Fatalf("explicit empty directory must be valid: %v", err)
	}
	if len(raw.Entries) != 0 {
		t.Fatalf("expected zero entries, got %d", len(raw.Entries))
	}
}

func TestScopedErrorLoginClassification(t *testing.T) {
	for _, tc := range []struct {
		code int
		want alist.ScopedErrorKind
	}{
		{401, alist.ScopedAuthOrPermission},
		{403, alist.ScopedAuthOrPermission},
		{429, alist.ScopedThrottled},
		{500, alist.ScopedTransientProvider},
		{503, alist.ScopedTransientProvider},
	} {
		srv := scopedCodeServer(t, tc.code)
		_, err := (alist.Adapter{BaseURL: srv.URL, Username: "u", Password: "p"}).
			ScanScope(context.Background(), "/x", 10)
		assertScopedKind(t, err, tc.want)
	}
}

func TestScopedErrorLoginTransportIsTransient(t *testing.T) {
	_, err := (alist.Adapter{BaseURL: "http://127.0.0.1:1", Username: "u", Password: "p"}).
		ScanScope(context.Background(), "/x", 10)
	assertScopedKind(t, err, alist.ScopedTransientProvider)
}

func TestScopedErrorLoginNoTokenIsTransient(t *testing.T) {
	// code=200 with data:null yields no token; that is a provider failure, not an
	// untyped INTERNAL leak.
	srv := scopedCodeServer(t, 200)
	_, err := (alist.Adapter{BaseURL: srv.URL, Username: "u", Password: "p"}).
		ScanScope(context.Background(), "/x", 10)
	assertScopedKind(t, err, alist.ScopedTransientProvider)
}
