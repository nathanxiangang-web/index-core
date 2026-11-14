package scan_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/rolling"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func TestIssue134RollingKernelAnchoredRetirementAndReappearance(t *testing.T) {
	for _, mode := range []string{"reappear", "operator-cold", "pending-hint", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			st, mock, root := setupScopedRoot(t)
			ctx := context.Background()
			var err error
			st, err = st.WithMirrorRolling(rolling.Policy{SharePercent: 25, Spacing: 200 * time.Millisecond, Lease: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("ROLLING_TOPOLOGY", "owned-only")
			var incomplete atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/me":
					io.WriteString(w, `{"code":200,"data":{"base_path":"/drive/library"}}`)
					return
				case "/api/admin/storage/list":
					io.WriteString(w, `{"code":200,"data":{"total":1,"content":[{"id":1,"mount_path":"/drive","driver":"115 Open","status":"work","disabled":false}]}}`)
					return
				}
				if incomplete.Load() {
					io.WriteString(w, `{"code":200,"data":{"total":3,"content":[]}}`)
					return
				}
				mock.handler()(w, r)
			}))
			defer srv.Close()
			cfg, _ := json.Marshal(map[string]any{"base_url": srv.URL, "path": "/", "mirror_presence": true, "expected_base_path": "/drive/library", "topology_token_env": "ROLLING_TOPOLOGY"})
			if err = st.UpsertAdapterConfig(ctx, root, postgres.AdapterConfig{CollectorKind: "openlist", Config: cfg}); err != nil {
				t.Fatal(err)
			}
			svc := scan.New(st, "", "", 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
			observe := func(scope string) {
				t.Helper()
				if _, e := svc.ScanScope(ctx, root, scope, 1000); e != nil {
					t.Fatal(e)
				}
			}
			complete := func(scope string, children []string) {
				t.Helper()
				now := time.Now().UTC()
				if _, e := st.MergeSignal(ctx, state.DirtySignal{RootID: root, ScopeKey: scope, Source: state.SourceRecovery, Reason: state.ReasonPossibleChange, Priority: state.PriorityNormal, SeenAt: now}); e != nil {
					t.Fatal(e)
				}
				w, e := st.GetWork(ctx, root, scope)
				if e != nil {
					t.Fatal(e)
				}
				c, e := st.ClaimWork(ctx, root, scope, w.Version, now)
				if e != nil {
					t.Fatal(e)
				}
				if _, _, e = st.CompleteSuccessWithMirrorChildren(ctx, root, scope, *c.ClaimedSignalSeq, children, "", now, 2*time.Minute); e != nil {
					t.Fatal(e)
				}
			}
			mock.set("/", dirEntry("gallery"), dirEntry("keep"))
			observe("/")
			complete("/", []string{"/gallery", "/keep"})
			mock.set("/gallery", fileEntry("photo.bin", 10, "unique-photo"))
			observe("/gallery")
			complete("/gallery", nil)
			w, err := st.GetWatch(ctx, root, "/gallery")
			if err != nil {
				t.Fatal(err)
			}
			incomplete.Store(true)
			if _, e := svc.ScanScope(ctx, root, "/", 1000); e == nil {
				t.Fatal("incomplete observation accepted")
			}
			incomplete.Store(false)
			when := time.Now().Add(2 * time.Hour)
			result, e := st.MaintainMirrorWatch(ctx, root, "/gallery", w.Version, 1, when)
			if e != nil || result != "EXPIRED" {
				t.Fatal("incomplete observation retired Watch", result, e)
			}
			if mode == "pending-hint" {
				if _, e = st.MergeSignal(ctx, state.DirtySignal{RootID: root, ScopeKey: "/gallery", Source: state.SourceMutationHint, Reason: state.ReasonPossibleChange, Priority: state.PriorityHigh, SeenAt: time.Now()}); e != nil {
					t.Fatal(e)
				}
			}
			mock.set("/", dirEntry("keep"))
			observe("/")
			var removed int
			if e = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE root_id=$1 AND canonical_path IN ('/gallery','/gallery/photo.bin') AND resource_presence='REMOVED'`, root).Scan(&removed); e != nil || removed != 2 {
				t.Fatal("real Kernel removal missing", removed, e)
			}
			var generation, events int64
			if e = st.Pool().QueryRow(ctx, `SELECT current_generation,(SELECT count(*) FROM index_journal_event WHERE root_id=$1) FROM index_root WHERE root_id=$1`, root).Scan(&generation, &events); e != nil {
				t.Fatal(e)
			}
			if mode == "pending-hint" {
				result, e = st.MaintainMirrorWatch(ctx, root, "/gallery", w.Version, 1, when)
				if e != nil || result == "RETIRED" {
					t.Fatal("pending Hint retired", result, e)
				}
				return
			}
			if mode == "replacement" {
				if _, e = st.Pool().Exec(ctx, `INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path,is_dir,name) VALUES('13440000-0000-4000-8000-000000000001',$1,0,0,'PRESENT','NONE','/gallery',true,'gallery')`, root); e != nil {
					t.Fatal(e)
				}
				result, e = st.MaintainMirrorWatch(ctx, root, "/gallery", w.Version, 1, when)
				if e != nil || result == "RETIRED" {
					t.Fatal("replacement path retired using old identity", result, e)
				}
				return
			}
			// Failure after Watch update must roll back both schedule and counters.
			if _, e = st.Pool().Exec(ctx, `CREATE FUNCTION rolling_abort() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.lifecycle='RETIRED' THEN RAISE EXCEPTION 'owned rollback injection'; END IF; RETURN NEW; END $$;CREATE TRIGGER rolling_abort BEFORE UPDATE ON index_mirror_watch_binding FOR EACH ROW EXECUTE FUNCTION rolling_abort()`); e != nil {
				t.Fatal(e)
			}
			if _, e = st.MaintainMirrorWatch(ctx, root, "/gallery", w.Version, 1, when); e == nil {
				t.Fatal("retirement rollback injection missed")
			}
			after, _ := st.GetWatch(ctx, root, "/gallery")
			if after.WatchState != state.WatchWarm || after.Version != w.Version {
				t.Fatal("partial retirement", after)
			}
			if _, e = st.Pool().Exec(ctx, `DROP TRIGGER rolling_abort ON index_mirror_watch_binding;DROP FUNCTION rolling_abort()`); e != nil {
				t.Fatal(e)
			}
			result, e = st.MaintainMirrorWatch(ctx, root, "/gallery", w.Version, 1, when)
			if e != nil || result != "RETIRED" {
				t.Fatal(result, e)
			}
			var g2, e2 int64
			if e = st.Pool().QueryRow(ctx, `SELECT current_generation,(SELECT count(*) FROM index_journal_event WHERE root_id=$1) FROM index_root WHERE root_id=$1`, root).Scan(&g2, &e2); e != nil || g2 != generation || e2 != events {
				t.Fatal("retirement changed canonical history", g2, e2, e)
			}
			if _, e = st.MaintainMirrorWatch(ctx, root, "/gallery", w.Version, 1, when); !errors.Is(e, postgres.ErrStateCASConflict) {
				t.Fatal("stale retirement CAS accepted", e)
			}
			retired, _ := st.GetWatch(ctx, root, "/gallery")
			if mode == "operator-cold" {
				if _, e = st.CASUpdateWatch(ctx, retired, retired.Version); e != nil {
					t.Fatal(e)
				}
			}
			mock.set("/", dirEntry("gallery"), dirEntry("keep"))
			observe("/")
			complete("/", []string{"/gallery"})
			reappeared, _ := st.GetWatch(ctx, root, "/gallery")
			if mode == "operator-cold" {
				if reappeared.WatchState != state.WatchCold {
					t.Fatal("operator override lost")
				}
			} else {
				if reappeared.WatchState != state.WatchWarm {
					t.Fatal("auto retired Watch did not rebind", reappeared)
				}
				work, e := st.GetWork(ctx, root, "/gallery")
				if e != nil || work.WorkState != state.WorkPending {
					t.Fatal("reappearance lost recovery", work, e)
				}
			}
			s, e := st.MirrorRollingStats(ctx, when)
			if e != nil || s.RetirementEvents != 1 || s.ExpirationEvents != 1 {
				t.Fatal(fmt.Sprint(s), e)
			}
		})
	}
}
