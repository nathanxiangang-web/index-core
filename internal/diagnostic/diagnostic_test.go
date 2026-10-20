package diagnostic

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestP0R3EvidenceClasses(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute)
	past := now.Add(-time.Hour)
	due := now.Add(-time.Second)
	future := now.Add(time.Minute)
	for _, tc := range []struct {
		name, code string
		mutate     func(*Report)
	}{
		{"healthy-durable-evidence", "BOUNDED_CONSISTENCY", func(r *Report) {}},
		{"inactive-root", "ROOT_INACTIVE", func(r *Report) { r.Lifecycle = "DEPRECATED" }},
		{"watch-missing", "WATCH_MISSING", func(r *Report) { r.Watch = nil }},
		{"watch-disabled", "WATCH_DISABLED", func(r *Report) { r.Watch.State = "DISABLED" }},
		{"watch-cold", "WATCH_DISABLED", func(r *Report) { r.Watch.State = "COLD" }},
		{"watch-due", "WATCH_DUE", func(r *Report) { r.Watch.NextDueAt = &due }},
		{"watch-lag", "WATCH_LAG", func(r *Report) { r.Watch.NextDueAt = &past }},
		{"watch-deferred", "WATCH_SCHEDULED", func(r *Report) { r.Watch.NextDueAt = &past; r.Watch.DeferredUntil = &future }},
		{"provider-failure", "SOURCE_UNAVAILABLE", func(r *Report) { r.Watch.Failures = 1; r.Watch.LastError = "TRANSIENT_PROVIDER" }},
		{"auth-failure", "SOURCE_UNAVAILABLE_WORK", func(r *Report) { r.Work.SourceFailures = 1 }},
		{"blocked", "BLOCKED_WORK", func(r *Report) { r.Work.Counts["BLOCKED"] = 1 }},
		{"suspended", "BLOCKED_WORK", func(r *Report) { r.Work.Counts["SUSPENDED"] = 1 }},
		{"cap-exhausted", "RETRY_BUDGET_EXHAUSTED", func(r *Report) { r.Work.InternalRetryCap = 1 }},
		{"retry-wait", "RETRY_WAIT", func(r *Report) { r.Work.Counts["RETRY_WAIT"] = 1 }},
		{"inflight", "IN_FLIGHT", func(r *Report) { r.Work.Counts["IN_FLIGHT"] = 1 }},
		{"stale-inflight", "STALE_IN_FLIGHT", func(r *Report) { r.Work.OldestInflightAt = &past }},
		{"stale-work", "WORK_LAG", func(r *Report) { r.Work.OldestDueAt = &past }},
		{"work-cap", "WORK_LIMIT", func(r *Report) { r.Work.Exact = false }},
		{"no-snapshot", "OBSERVATION_MISSING", func(r *Report) { r.Latest = nil }},
		{"stale-observation", "SOURCE_STALE", func(r *Report) { r.Latest.At = past }},
		{"future-observation", "OBSERVATION_FUTURE", func(r *Report) { r.Latest.At = future }},
		{"failed-observation", "OBSERVATION_FAILED", func(r *Report) { r.Latest.Traversal = "FAILED" }},
		{"partial-listing", "INCOMPLETE_LISTING", func(r *Report) { r.Latest.Completeness = "PARTIAL" }},
		{"child-scope", "ROOT_FRESHNESS", func(r *Report) { r.Latest.Scope = "CHILD" }},
		{"untrusted-topology", "TOPOLOGY_UNTRUSTED", func(r *Report) { r.Mirror = true }},
		{"historical-proof", "HISTORICAL_COVERAGE", func(r *Report) { r.Mirror = true; r.Latest.HistoricalCoverageV2 = true }},
		{"duplicate-path", "IDENTITY_AMBIGUITY", func(r *Report) { r.Inventory.DuplicatePresentPaths = 1 }},
		{"conflict", "IDENTITY_AMBIGUITY", func(r *Report) { r.Inventory.LatestConflictCount = 1 }},
		{"canonical-cap", "IDENTITY_LIMIT", func(r *Report) { r.Inventory.CanonicalExact = false }},
		{"journal-cap", "CONSISTENCY_LIMIT", func(r *Report) { r.Inventory.JournalExact = false }},
		{"journal-mismatch", "CANONICAL_JOURNAL_MISMATCH", func(r *Report) { r.Inventory.Mismatch = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New("00000000-0000-0000-0000-000000000001", Defaults())
			r.Complete = true
			r.AsOf = now
			r.Lifecycle = "ACTIVE"
			r.Watch = &Watch{State: "HOT", NextDueAt: &future}
			r.Work.Exact = true
			r.Latest = &Observation{At: fresh, Scope: "ROOT", Traversal: "SUCCESS", Completeness: "COMPLETE", Acceptance: "COMPLETE", Freshness: "FRESH_DIRECT", Reconciled: true}
			r.Inventory.CanonicalExact = true
			r.Inventory.JournalExact = true
			tc.mutate(&r)
			r.Evaluate(Defaults())
			found := false
			for _, c := range r.Checks {
				if c.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s", tc.code)
			}
			if r.Overall == "OK" {
				t.Fatal("database evidence cannot prove live-source or downstream health")
			}
			a, _ := json.Marshal(r)
			b, _ := json.Marshal(r)
			if string(a) != string(b) {
				t.Fatal("unstable JSON")
			}
			if !strings.Contains(string(a), `"code":"CONSUMER_PROJECTION_LAG","status":"NOT_TRACKED"`) {
				t.Fatal("invented downstream cursor")
			}
		})
	}
}

func TestP0R3Limits(t *testing.T) {
	for _, mutate := range []func(*Options){func(o *Options) { o.RowLimit = 0 }, func(o *Options) { o.RowLimit = 10001 }, func(o *Options) { o.Timeout = 0 }, func(o *Options) { o.Timeout = 31 * time.Second }, func(o *Options) { o.StaleAfter = 0 }, func(o *Options) { o.StaleAfter = 25 * time.Hour }} {
		o := Defaults()
		mutate(&o)
		if o.Validate() == nil {
			t.Fatal("unsafe bound accepted")
		}
	}
}
