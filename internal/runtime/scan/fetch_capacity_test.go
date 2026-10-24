package scan_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
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

type f138ReadResult struct {
	root, scope string
	f           *scan.FetchedScope
	err         error
	claim       state.DirtyScopeWork
	ack         chan struct{}
}

func f138Quantile(v []float64, q float64) float64 {
	sort.Float64s(v)
	return v[int(math.Ceil(float64(len(v))*q))-1]
}

// A test-only driver, not app/runtime wiring. Each worker owns at most one
// fetched result until the serial caller acknowledges commit. Jobs <=2N,
// results <=N, workers=N. One claimed scope per root in a wave; there is no
// claim that this solves same-root speculative admission or F4 debt.
func TestIssue138CapacityReadPrototype(t *testing.T) {
	rawScale := os.Getenv("INDEXCORE_ISSUE138_SCALE")
	if rawScale == "" {
		t.Skip("opt-in actual delayed HTTP capacity fixture")
	}
	n, err := strconv.Atoi(rawScale)
	if err != nil || (n != 2000 && n != 20000 && n != 50000) {
		t.Fatal("scale must be2000/20000/50000")
	}
	testutil.Issue138Pool(t)
	baseline := 0.0
	for _, workers := range []int{1, 2, 4, 8} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			pool := testutil.Issue138Pool(t)
			testutil.ResetSchema(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if err := postgres.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			st := postgres.New(pool)
			roots := make([]string, 8)
			changed := map[string]bool{}
			for r := 0; r < 8; r++ {
				for d := 0; d < 2; d++ {
					changed[fmt.Sprintf("/r%02d/d%06d", r, d)] = true
				}
			}
			for d := 2; len(changed) < n/100; d++ {
				changed[fmt.Sprintf("/r00/d%06d", d)] = true
			}
			var calls, inflight, peak, waitNS, duplicates atomic.Int64
			var seen sync.Map
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/fs/list" {
					http.NotFound(w, r)
					return
				}
				var req struct {
					Path    string `json:"path"`
					Refresh bool   `json:"refresh"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil || !req.Refresh {
					http.Error(w, "invalid fixture request", 400)
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
				waitNS.Add(int64(time.Since(start)))
				entries := []map[string]any{}
				if changed[req.Path] {
					entries = append(entries, fileEntry("late.bin", 10, ""))
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": entries, "total": len(entries)}})
			}))
			defer srv.Close()
			for i := range roots {
				roots[i] = fmt.Sprintf("f1380000-0000-4000-8000-%012d", i+1)
				if err := st.CreateRoot(ctx, pool, roots[i], []byte(`{}`), domain.RootActive); err != nil {
					t.Fatal(err)
				}
				if err := st.UpsertRootPolicy(ctx, roots[i], postgres.RootPolicy{RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour, MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}); err != nil {
					t.Fatal(err)
				}
				cfg, _ := json.Marshal(map[string]any{"base_url": srv.URL, "path": fmt.Sprintf("/r%02d", i)})
				if err := st.UpsertAdapterConfig(ctx, roots[i], postgres.AdapterConfig{CollectorKind: "openlist", Config: cfg}); err != nil {
					t.Fatal(err)
				}
			}
			// Seed directory population ONLY, not source scans or accepted events.
			i := 0
			perRoot := n / 8
			rows, err := pool.CopyFrom(ctx, pgx.Identifier{"index_canonical_resource"}, []string{"resource_id", "root_id", "introduced_at_generation", "last_confirmed_generation", "resource_presence", "removal_evidence_state", "canonical_path", "name", "is_dir", "size", "mtime"}, pgx.CopyFromFunc(func() ([]any, error) {
				if i == n {
					return nil, nil
				}
				r, d := i/perRoot, i%perRoot
				i++
				return []any{fmt.Sprintf("f1381000-0000-4000-8000-%012x", i), roots[r], 0, 0, "PRESENT", "NONE", fmt.Sprintf("/r%02d/d%06d", r, d), fmt.Sprintf("d%06d", d), true, int64(0), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
			}))
			if err != nil || rows != int64(n) {
				t.Fatal("population seed", rows, err)
			}
			for _, root := range roots {
				for d := 0; d < 50; d++ {
					_, err := st.MergeSignal(ctx, state.DirtySignal{RootID: root, ScopeKey: fmt.Sprintf("/d%06d", d), Source: state.SourceFullVerifyBackstop, Reason: state.ReasonDriftVerify, Priority: state.PriorityNormal, SeenAt: time.Now().UTC().Add(-2 * time.Hour)})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			p, err := scan.NewFetchPrototype(scan.New(st, "", "", 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil))), workers)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			var qLatency, commitLatency, readLatency []float64
			blockedPeak := 0
			for wave := 0; wave < 2; wave++ {
				jobs := make(chan f138ReadResult, 2*workers)
				results := make(chan f138ReadResult, workers)
				var wg sync.WaitGroup
				for w := 0; w < workers; w++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for {
							var job f138ReadResult
							select {
							case <-ctx.Done():
								return
							case next, ok := <-jobs:
								if !ok {
									return
								}
								job = next
							}
							job.f, job.err = p.FetchScope(ctx, job.claim, 1000)
							select {
							case results <- job:
							case <-ctx.Done():
								if job.f != nil {
									p.DiscardFetchedScope(job.f)
								}
								return
							}
							select {
							case <-job.ack:
							case <-ctx.Done():
								return
							}
						}
					}()
				}
				// Claim serially using existing Store, not another queue protocol.
				// Worker goroutines above never mutate Work or Canonical.
				batch := make([]f138ReadResult, 8)
				for r, root := range roots {
					scope := fmt.Sprintf("/d%06d", wave)
					batch[r] = f138ReadResult{root: root, scope: scope, claim: f138Claim(t, st, root, scope), ack: make(chan struct{})}
				}
				go func() {
					defer func() { close(jobs); wg.Wait(); close(results) }()
					for _, job := range batch {
						select {
						case jobs <- job:
						case <-ctx.Done():
							return
						}
					}
				}()
				// Drain even on a failed result; no orphaned producer/worker.
				for job := range results {
					if job.err != nil {
						t.Error("fetch", job.root, job.err)
						close(job.ack)
						continue
					}
					readLatency = append(readLatency, job.f.Stats().ReadDuration.Seconds())
					commitStart := time.Now()
					res, err := p.CommitFetchedScope(ctx, job.f)
					commitLatency = append(commitLatency, time.Since(commitStart).Seconds())
					if err != nil || res.Outcome.Status != domain.AdmissionApplied {
						t.Error("commit", res, err)
						close(job.ack)
						continue
					}
					if _, err := st.CompleteSuccess(ctx, job.root, job.scope, *job.claim.ClaimedSignalSeq, time.Now().UTC()); err != nil {
						t.Error(err)
					}
					rootNum := strings.TrimPrefix(job.root, "f1380000-0000-4000-8000-")
					rNum, _ := strconv.Atoi(rootNum)
					target := fmt.Sprintf("/r%02d%s/late.bin", rNum-1, job.scope)
					qr := postgres.NewQueryReader(pool)
					resolution, err := qr.ResolvePath(ctx, job.root, target, query.ReadOptions{})
					if err != nil || len(resolution.Matches) != 1 {
						t.Error("Q5", target, resolution, err)
						close(job.ack)
						continue
					}
					var producing string
					if err := pool.QueryRow(ctx, `SELECT g.produced_by_snapshot_id::text FROM index_generation g JOIN index_canonical_resource c ON c.root_id=g.root_id AND c.introduced_at_generation=g.generation_number WHERE c.resource_id=$1`, resolution.Matches[0].ResourceID).Scan(&producing); err != nil || producing != res.SnapshotID {
						t.Error("wrong producing Snapshot", producing, res.SnapshotID, err)
					}
					journal, err := qr.ReadJournal(ctx, job.root, 0, 10)
					if err != nil || len(journal) != wave+1 {
						t.Error("Q8", journal, err)
					}
					for i, event := range journal {
						if event.EventSeq != int64(i+1) || event.GenerationNumber != int64(i+1) || event.EventType != domain.EventResourceAdded {
							t.Error("Journal ordering/removal", event)
						}
					}
					if len(journal) > 0 && (journal[len(journal)-1].ResourceID == nil || *journal[len(journal)-1].ResourceID != resolution.Matches[0].ResourceID) {
						t.Error("Q5/Q8 identity mismatch", target)
					}
					qLatency = append(qLatency, time.Since(start).Seconds())
					var blocked int
					if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&blocked); err != nil {
						t.Error(err)
					}
					if blocked > blockedPeak {
						blockedPeak = blocked
					}
					close(job.ack)
				}
			}
			elapsed := time.Since(start).Seconds()
			if workers == 1 {
				baseline = elapsed
			}
			var pending, verified int
			if err := pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE work_state='PENDING'),count(*) FILTER(WHERE work_state='VERIFIED') FROM index_dirty_scope_work`).Scan(&pending, &verified); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 16 || duplicates.Load() != 0 || peak.Load() > int64(workers) || inflight.Load() != 0 || verified != 16 || pending != 384 || f138Count(t, st, "index_canonical_resource") != n+16 || f138Count(t, st, "index_journal_event") != 16 {
				t.Fatal("fixture integrity", calls.Load(), peak.Load(), verified, pending)
			}
			if len(qLatency) != 16 {
				t.Fatal("incomplete observed sample", len(qLatency))
			}
			if workers == 4 && baseline/elapsed <= 1.5 {
				t.Error("NO-GO: four-reader prototype did not exceed1.5x baseline", baseline/elapsed)
			}
			var dbSize, tempBytes int64
			var tempFiles int64
			if err := pool.QueryRow(ctx, `SELECT pg_database_size(current_database()),temp_files,temp_bytes FROM pg_stat_database WHERE datname=current_database()`).Scan(&dbSize, &tempFiles, &tempBytes); err != nil {
				t.Fatal(err)
			}
			out, _ := json.Marshal(map[string]any{"preseeded_directories": n, "roots": 8, "workers": workers, "actual_forced_lists": calls.Load(), "peak_source_inflight": peak.Load(), "wall_seconds": elapsed, "speedup_vs1": baseline / elapsed, "summed_http_wait_seconds": float64(waitNS.Load()) / 1e9, "source_changed": len(changed), "observed_changed": 16, "unobserved_changed": len(changed) - 16, "pending_before": 400, "pending_after": pending, "verified": verified, "sampled_blocked_peak": blockedPeak, "read_p95_seconds": f138Quantile(readLatency, .95), "commit_p95_seconds": f138Quantile(commitLatency, .95), "batch_detect_to_q5q8_p50_seconds": f138Quantile(qLatency, .5), "batch_detect_to_q5q8_p95_seconds": f138Quantile(qLatency, .95), "batch_detect_to_q5q8_p99_seconds": f138Quantile(qLatency, .99), "latency_samples": len(qLatency), "database_bytes": dbSize, "temp_files": tempFiles, "temp_bytes": tempBytes})
			t.Log("ISSUE138_CAPACITY " + string(out))
		})
	}
}
