package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/sourcebudget"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// Separate explicitly authorized source-read proof, never used by default/CI.
// Only the owner-marked disposable DB is writable. No source mutation routes.
func TestIssue134RealSourceReadOnly(t *testing.T) {
	if os.Getenv("INDEXCORE_ISSUE134_REAL_READONLY") != "owned-db-source-readonly" {
		t.Skip("explicit live-source read authorization and owned PG18 required")
	}
	pool := testutil.Issue138Pool(t)
	var secret struct {
		Origin, Token, BasePath string
		Chosen                  struct{ Path string }
	}
	b, e := os.ReadFile(os.Getenv("INDEXCORE_REAL_SOURCE_PRIVATE_CONFIG"))
	if e != nil {
		t.Fatal("private source configuration unavailable")
	}
	if json.Unmarshal(b, &secret) != nil || secret.Origin != "https://pan.netioi.com" || secret.Token == "" || secret.Chosen.Path == "" {
		t.Fatal("refuses ambiguous real-source config")
	}
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if e = postgres.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	st := postgres.New(pool)
	root := "b1340000-0000-4000-8000-000000000001"
	if e = st.CreateRoot(ctx, pool, root, []byte(`{}`), domain.RootActive); e != nil {
		t.Fatal(e)
	}
	if e = st.UpsertRootPolicy(ctx, root, postgres.RootPolicy{RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour, MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}); e != nil {
		t.Fatal(e)
	}
	lock, e := st.AcquireWriterLock(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Release(ctx)
	dir := os.Getenv("INDEXCORE_REPLAY_EVIDENCE_DIR")
	if dir == "" {
		t.Fatal("requires private evidence directory")
	}
	out, e := os.OpenFile(filepath.Join(dir, "real-source-readonly.ndjson"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer out.Close()
	var mu sync.Mutex
	var evidenceErr error
	calls := 0
	write := func(v map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		v["at"] = time.Now().UTC()
		if e := json.NewEncoder(out).Encode(v); e != nil {
			evidenceErr = e
		}
	}
	u, _ := url.Parse(secret.Origin)
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.ModifyResponse = func(r *http.Response) error {
		write(map[string]any{"kind": "http_response", "status": r.StatusCode, "endpoint": r.Request.URL.Path})
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/fs/list" {
			http.Error(w, "source route denied", 403)
			return
		}
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n > 3 {
			http.Error(w, "source request cap", 429)
			return
		}
		write(map[string]any{"kind": "http_start", "endpoint": r.URL.Path})
		r.Host = u.Host
		proxy.ServeHTTP(w, r)
	}))
	defer server.Close()
	t.Setenv("ISSUE134_REAL_TOKEN", secret.Token)
	cfg, _ := json.Marshal(map[string]any{"base_url": server.URL, "path": secret.Chosen.Path, "token_env": "ISSUE134_REAL_TOKEN", "mirror_presence": false})
	if e = st.UpsertAdapterConfig(ctx, root, postgres.AdapterConfig{CollectorKind: "openlist", Config: cfg}); e != nil {
		t.Fatal(e)
	}
	bc := sourcebudget.Defaults()
	bc.Global = time.Second
	bc.Root = time.Second
	bc.Provider = time.Second
	budget, e := sourcebudget.New(bc)
	if e != nil {
		t.Fatal(e)
	}
	scanner := scan.New(st, "", "", 30*time.Second, poolLogger()).WithSourceBudget(budget)
	qr := postgres.NewQueryReader(pool)
	identities := map[string]string{}
	seq := int64(0)
	start := time.Now()
	for round := 0; round < 3; round++ {
		result, e := scanner.ScanScope(ctx, root, "/", 1000)
		if e != nil {
			t.Fatal("real source scan failed; raw source error withheld")
		}
		rows, e := pool.Query(ctx, `SELECT canonical_path,resource_id::text FROM index_canonical_resource WHERE root_id=$1 AND resource_presence='PRESENT'`, root)
		if e != nil {
			t.Fatal(e)
		}
		paths := map[string]string{}
		for rows.Next() {
			var path, id string
			if e = rows.Scan(&path, &id); e != nil {
				t.Fatal(e)
			}
			paths[path] = id
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		if len(paths) == 0 {
			t.Fatal("real source returned no canonical resources")
		}
		for path, id := range paths {
			if prior, ok := identities[path]; ok && prior != id {
				t.Fatal("real source identity changed")
			}
			resolved, e := qr.ResolvePath(ctx, root, path, query.ReadOptions{})
			if e != nil || resolved.Ambiguous || len(resolved.Matches) != 1 || resolved.Matches[0].ResourceID != id {
				t.Fatal("real source Q5 identity")
			}
		}
		events, e := qr.ReadJournal(ctx, root, seq, 1000)
		if e != nil {
			t.Fatal(e)
		}
		for _, ev := range events {
			if ev.EventSeq != seq+1 || ev.EventType == domain.EventResourceRemoved || ev.ResourceID == nil {
				t.Fatal("real source Q8 ordering/absence")
			}
			seq = ev.EventSeq
		}
		identities = paths
		write(map[string]any{"kind": "round", "round": round, "snapshot": result.SnapshotID, "canonical_count": len(paths), "q8_sequence": seq, "status": result.Outcome.Status, "elapsed_seconds": time.Since(start).Seconds()})
	}
	if calls != 3 || evidenceErr != nil {
		t.Fatal("invalid source/evidence request accounting")
	}
	hash := sha256.Sum256([]byte(secret.Chosen.Path))
	summary := map[string]any{"kind": "summary", "canonical_count": len(identities), "q8_sequence": seq, "http_requests": calls, "source_writes": 0, "readers": 1, "spacing_ms": 1000, "selected_path_sha256": hex.EncodeToString(hash[:]), "topology_proof": "NOT_AVAILABLE_ACCOUNT_403", "mirror_presence": false, "new_file_injection": "NOT_RUN", "f1_f2_performance": "NOT_VERIFIED"}
	write(summary)
	t.Log(fmt.Sprint(summary))
}
