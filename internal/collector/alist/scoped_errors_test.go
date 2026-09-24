package alist_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
