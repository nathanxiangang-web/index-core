package scan_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// Actual same-Root HTTP reads, frozen barriers and real ordered Kernel commits.
// This test driver is NOT runtime wiring. Population is NOT traversal count.
func TestIssue138SameRootCapacity(t *testing.T) {
	raw := os.Getenv("INDEXCORE_ISSUE138_SAME_ROOT_FETCHES")
	if raw == "" {
		t.Skip("opt-in owned PG18 same-root delayed source matrix")
	}
	count, err := strconv.Atoi(raw)
	if err != nil || (count != 16 && count != 64 && count != 400) {
		t.Fatal("actual fetches must be16/64/400")
	}
	n, err := strconv.Atoi(os.Getenv("INDEXCORE_ISSUE138_SAME_ROOT_POPULATION"))
	if err != nil || (n != 2000 && n != 20000 && n != 50000) {
		t.Fatal("population must be2k/20k/50k")
	}
	testutil.Issue138Pool(t)
	baseline := 0.0
	for _, workers := range []int{1, 2, 4, 8} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			pool := testutil.Issue138Pool(t)
			testutil.ResetSchema(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
			defer cancel()
			if err := postgres.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			st := postgres.New(pool)
			root := "f1382000-0000-4000-8000-000000000001"
			if err := st.CreateRoot(ctx, pool, root, []byte(`{}`), domain.RootActive); err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertRootPolicy(ctx, root, postgres.RootPolicy{RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour, MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}); err != nil {
				t.Fatal(err)
			}
			var calls, apiCalls, inflight, peak, duplicates atomic.Int64
			var seen sync.Map
			var timesMu sync.Mutex
			times := map[string][2]time.Time{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				apiCalls.Add(1)
				switch r.URL.Path {
				case "/api/me":
					io.WriteString(w, `{"code":200,"data":{"base_path":"/drive/library"}}`)
					return
				case "/api/admin/storage/list":
					io.WriteString(w, `{"code":200,"data":{"total":1,"content":[{"id":1,"mount_path":"/drive","driver":"115 Open","status":"work","disabled":false}]}}`)
					return
				case "/api/fs/list":
					var req struct {
						Path    string
						Refresh bool
					}
					if json.NewDecoder(r.Body).Decode(&req) != nil || !req.Refresh {
						http.Error(w, "invalid fixture request", 400)
						return
					}
					d, err := strconv.Atoi(strings.TrimPrefix(req.Path, "/d"))
					if err != nil || d < 0 || d >= 512 {
						http.Error(w, "unoffered scope", 400)
						return
					}
					calls.Add(1)
					if _, loaded := seen.LoadOrStore(req.Path, true); loaded {
						duplicates.Add(1)
					}
					cur := inflight.Add(1)
					defer inflight.Add(-1)
					for old := peak.Load(); cur > old; old = peak.Load() {
						if peak.CompareAndSwap(old, cur) {
							break
						}
					}
					start := time.Now()
					timer := time.NewTimer(1250 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-r.Context().Done():
						return
					}
					entries := []map[string]any{fileEntry("late.bin", int64(100000+d), fmt.Sprintf("unique-%d", d))}
					json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": entries, "total": 1}})
					timesMu.Lock()
					times[req.Path] = [2]time.Time{start, time.Now()}
					timesMu.Unlock()
					return
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			t.Setenv("ISSUE138_LINEAGE_TOPOLOGY", "fixture-only")
			config, _ := json.Marshal(map[string]any{"base_url": srv.URL, "path": "/", "mirror_presence": true, "expected_base_path": "/drive/library", "topology_token_env": "ISSUE138_LINEAGE_TOPOLOGY"})
			if err := st.UpsertAdapterConfig(ctx, root, postgres.AdapterConfig{CollectorKind: "openlist", Config: config}); err != nil {
				t.Fatal(err)
			}
			i := 0
			seeded, err := pool.CopyFrom(ctx, pgx.Identifier{"index_canonical_resource"}, []string{"resource_id", "root_id", "introduced_at_generation", "last_confirmed_generation", "resource_presence", "removal_evidence_state", "canonical_path", "name", "is_dir", "size", "mtime"}, pgx.CopyFromFunc(func() ([]any, error) {
				if i == n {
					return nil, nil
				}
				j := i
				i++
				return []any{fmt.Sprintf("f1383000-0000-4000-8000-%012x", i), root, 0, 0, "PRESENT", "NONE", fmt.Sprintf("/d%06d", j), fmt.Sprintf("d%06d", j), true, int64(0), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
			}))
			if err != nil || seeded != int64(n) {
				t.Fatal("SQL population seed", seeded, err)
			}
			work := make([]state.DirtyScopeWork, 512)
			for i := range work {
				work[i], err = st.MergeSignal(ctx, state.DirtySignal{RootID: root, ScopeKey: fmt.Sprintf("/d%06d", i), Source: state.SourceFullVerifyBackstop, Reason: state.ReasonDriftVerify, Priority: state.PriorityNormal, SeenAt: time.Now().UTC().Add(-time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
			}
			p, err := scan.NewSameRootPrototype(scan.New(st, "", "", 90*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil))), workers)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			queue, reads, commits, visible, postSource := []float64{}, []float64{}, []float64{}, []float64{}, []float64{}
			qr := postgres.NewQueryReader(pool)
			for offset := 0; offset < count; offset += 8 {
				claims := make([]state.DirtyScopeWork, 8)
				for j := range claims {
					w := work[offset+j]
					claims[j], err = st.ClaimWork(ctx, root, w.ScopeKey, w.Version, time.Now().UTC())
					if err != nil {
						t.Fatal(err)
					}
					queue = append(queue, time.Since(start).Seconds())
				}
				b, err := p.FetchBatch(ctx, claims)
				if err != nil {
					t.Fatal("fetch/freeze", offset, err)
				}
				for rank, c := range claims {
					began := time.Now()
					res, err := p.Commit(ctx, b, rank)
					commits = append(commits, time.Since(began).Seconds())
					if err != nil {
						b.Close()
						t.Fatal("ordered commit", offset, rank, err)
					}
					if res.Outcome.Status != domain.AdmissionApplied || res.Outcome.Generation != int64(offset+rank) {
						b.Close()
						t.Fatal("generation order", res)
					}
					if _, err := st.CompleteSuccess(ctx, root, c.ScopeKey, *c.ClaimedSignalSeq, time.Now().UTC()); err != nil {
						b.Close()
						t.Fatal(err)
					}
					target := c.ScopeKey + "/late.bin"
					resolved, err := qr.ResolvePath(ctx, root, target, query.ReadOptions{})
					if err != nil || len(resolved.Matches) != 1 {
						b.Close()
						t.Fatal("Q5", target, resolved, err)
					}
					events, err := qr.ReadJournal(ctx, root, int64(offset+rank), 2)
					if err != nil || len(events) != 1 || events[0].EventType != domain.EventResourceAdded || events[0].GenerationNumber != int64(offset+rank+1) || events[0].ResourceID == nil || *events[0].ResourceID != resolved.Matches[0].ResourceID {
						b.Close()
						t.Fatal("Q8 identity/order", events, err)
					}
					var producing string
					if err := pool.QueryRow(ctx, `SELECT produced_by_snapshot_id::text FROM index_generation WHERE root_id=$1::uuid AND generation_number=$2`, root, events[0].GenerationNumber).Scan(&producing); err != nil || producing != res.SnapshotID {
						b.Close()
						t.Fatal("wrong producing input", producing, res.SnapshotID, err)
					}
					timesMu.Lock()
					source := times[c.ScopeKey]
					timesMu.Unlock()
					if source[0].IsZero() || source[1].Before(source[0]) {
						b.Close()
						t.Fatal("missing real source timing")
					}
					reads = append(reads, source[1].Sub(source[0]).Seconds())
					visible = append(visible, time.Since(start).Seconds())
					postSource = append(postSource, time.Since(source[1]).Seconds())
				}
				b.Close()
			}
			wall := time.Since(start).Seconds()
			if workers == 1 {
				baseline = wall
			}
			var pending, verified, roots int
			if err := pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE work_state='PENDING'),count(*) FILTER(WHERE work_state='VERIFIED') FROM index_dirty_scope_work`).Scan(&pending, &verified); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM index_root`).Scan(&roots); err != nil {
				t.Fatal(err)
			}
			if roots != 1 || pending != 512-count || verified != count || calls.Load() != int64(count) || duplicates.Load() != 0 || peak.Load() > int64(workers) || inflight.Load() != 0 || f138Count(t, st, "index_canonical_resource") != n+count || f138Count(t, st, "index_journal_event") != count || f138Count(t, st, "index_snapshot") != count {
				t.Fatal("one-root integrity", roots, pending, verified, calls.Load(), duplicates.Load(), peak.Load())
			}
			if workers == 4 && baseline/wall <= 1.5 {
				t.Error("NO-GO: same-root four readers below1.5x", baseline/wall)
			}
			var bytes, tempBytes, tempFiles int64
			if err := pool.QueryRow(ctx, `SELECT pg_database_size(current_database()),temp_bytes,temp_files FROM pg_stat_database WHERE datname=current_database()`).Scan(&bytes, &tempBytes, &tempFiles); err != nil {
				t.Fatal(err)
			}
			receipt := map[string]any{"roots": roots, "preseeded_directories": n, "queued_scopes": 512, "actual_forced_lists": calls.Load(), "all_http_calls_including_topology": apiCalls.Load(), "offered_changed": 512, "observed_changed": count, "unobserved_changed": 512 - count, "workers": workers, "batch_width": 8, "peak_source_inflight": peak.Load(), "duplicate_lists": duplicates.Load(), "verified": verified, "pending": pending, "wall_seconds": wall, "speedup_vs1": baseline / wall, "queue_wait_p50": f138Quantile(queue, .5), "queue_wait_p95": f138Quantile(queue, .95), "queue_wait_p99": f138Quantile(queue, .99), "read_p50": f138Quantile(reads, .5), "read_p95": f138Quantile(reads, .95), "read_p99": f138Quantile(reads, .99), "commit_p50": f138Quantile(commits, .5), "commit_p95": f138Quantile(commits, .95), "commit_p99": f138Quantile(commits, .99), "source_to_q5q8_p50": f138Quantile(visible, .5), "source_to_q5q8_p95": f138Quantile(visible, .95), "source_to_q5q8_p99": f138Quantile(visible, .99), "http_complete_to_q5q8_p95": f138Quantile(postSource, .95), "database_bytes": bytes, "temp_bytes": tempBytes, "temp_files": tempFiles}
			raw, _ := json.Marshal(receipt)
			t.Log("ISSUE138_SAME_ROOT " + string(raw))
		})
	}
}
