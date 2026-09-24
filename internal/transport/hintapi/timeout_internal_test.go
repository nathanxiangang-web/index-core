package hintapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalhint"
)

type p9TimeoutIngester struct{ calls int }

func (p *p9TimeoutIngester) IngestOne(ctx context.Context, _ incrementalhint.Request) (state.DirtyScopeWork, error) {
	p.calls++
	<-ctx.Done()
	return state.DirtyScopeWork{}, ctx.Err()
}

// TestP9IngestTimeoutCancelsWithoutRetry shortens the package timeout so the
// bounded 5s prototype behaviour can be proven quickly.
func TestP9IngestTimeoutCancelsWithoutRetry(t *testing.T) {
	prev := ingestTimeout
	ingestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { ingestTimeout = prev })

	ing := &p9TimeoutIngester{}
	s, err := New(Deps{Ingester: ing, Token: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	body, _ := json.Marshal(map[string]string{"root_id": "11111111-1111-4111-8111-111111111111", "scope_key": "/a"})
	req, _ := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+HintPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+"0123456789abcdef0123456789abcdef")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d (%s)", resp.StatusCode, b)
	}
	if ing.calls != 1 {
		t.Fatalf("timeout must not retry, calls = %d", ing.calls)
	}
}
