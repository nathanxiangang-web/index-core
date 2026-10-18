package incrementalexec_test

import (
	"context"
	"encoding/json"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMirrorRootDiscoversChildrenWithoutGlobalDiscoveryPrefix(t *testing.T) {
	st, mock, id := p4SetupRootPath(t, "/")
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/me" {
			json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]string{"base_path": "/drive/library"}})
			return
		}
		mock.handler()(w, r)
	}))
	defer server.Close()
	config, _ := json.Marshal(map[string]any{"path": "/", "base_url": server.URL, "mirror_presence": true, "expected_base_path": "/drive/library"})
	if err := st.UpsertAdapterConfig(ctx, id, postgres.AdapterConfig{CollectorKind: "openlist", Config: config}); err != nil {
		t.Fatal(err)
	}
	mock.set("/", p4Dir("gallery"))
	p4SeedPending(t, st, ctx, id, "/", p4Now, nil)
	ex := p4Executor(t, st, p4RealScanner(st), p4Now)
	if _, err := ex.ExecuteOne(ctx); err != nil {
		t.Fatal(err)
	}
	w, err := st.GetWork(ctx, id, "/gallery")
	if err != nil || w.WorkState != state.WorkPending {
		t.Fatalf("child work %+v %v", w, err)
	}
}
