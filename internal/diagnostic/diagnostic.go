// Package diagnostic defines bounded operator evidence, not Canonical authority.
// It has no provider, writer, scheduler or HTTP dependencies.
package diagnostic

import (
	"errors"
	"time"
)

type Options struct {
	RowLimit   int
	Timeout    time.Duration
	StaleAfter time.Duration
}

func Defaults() Options { return Options{1000, 5 * time.Second, 5 * time.Minute} }
func (o Options) Validate() error {
	if o.RowLimit < 1 || o.RowLimit > 10000 || o.Timeout < time.Millisecond || o.Timeout > 30*time.Second || o.StaleAfter < time.Second || o.StaleAfter > 24*time.Hour {
		return errors.New("diagnostic limits: rows 1..10000, timeout 1ms..30s, stale-after 1s..24h")
	}
	return nil
}

// All strings emitted by the reader are constants or schema-constrained enums.
// Raw paths, config, errors, journal payloads and snapshot provenance are omitted.
type Check struct {
	Code   string `json:"code"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}
type Watch struct {
	State           string     `json:"state"`
	Priority        string     `json:"priority"`
	Cadence         string     `json:"cadence_category"`
	IntervalSeconds *int64     `json:"interval_seconds"`
	NextDueAt       *time.Time `json:"next_due_at"`
	DeferredUntil   *time.Time `json:"deferred_until"`
	LastStartedAt   *time.Time `json:"last_started_at"`
	LastFinishedAt  *time.Time `json:"last_finished_at"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
	Failures        int64      `json:"consecutive_failures"`
	LastError       string     `json:"last_error_class"`
}
type Work struct {
	Counts           map[string]int `json:"state_counts"`
	Exact            bool           `json:"counts_exact"`
	OldestDueAt      *time.Time     `json:"oldest_due_at"`
	OldestInflightAt *time.Time     `json:"oldest_inflight_at"`
	// Counts of qualifying work rows, never sums of attempts or failure events.
	SourceFailures   int `json:"work_items_with_source_failures"`
	InternalRetryCap int `json:"mirror_internal_retry_cap_work_items"`
}
type Observation struct {
	At                   time.Time `json:"observed_at"`
	Scope                string    `json:"scope_category"`
	Traversal            string    `json:"traversal"`
	Acceptance           string    `json:"acceptance"`
	Completeness         string    `json:"completeness"`
	Freshness            string    `json:"freshness"`
	HistoricalCoverageV2 bool      `json:"historical_coverage_v2_present"`
	Reconciled           bool      `json:"reconciled"`
}
type Inventory struct {
	CanonicalSample       int   `json:"canonical_rows_checked"`
	JournalSample         int   `json:"journal_rows_checked"`
	CanonicalExact        bool  `json:"canonical_complete"`
	JournalExact          bool  `json:"journal_complete"`
	JournalHighWatermark  int64 `json:"journal_high_watermark"`
	JournalMaxGeneration  int64 `json:"journal_max_generation"`
	DuplicatePresentPaths int   `json:"duplicate_present_path_groups"`
	LatestConflictCount   int   `json:"latest_reconcile_conflicts"`
	Mismatch              bool  `json:"consistency_mismatch"`
}
type Report struct {
	Version           int          `json:"version"`
	RootID            string       `json:"root_id"`
	AsOf              time.Time    `json:"as_of"`
	Overall           string       `json:"overall"`
	Complete          bool         `json:"query_complete"`
	RowLimit          int          `json:"row_limit_per_dataset"`
	StaleAfterSeconds int64        `json:"stale_after_seconds"`
	SchemaApplied     int          `json:"schema_applied"`
	SchemaRequired    int          `json:"schema_required"`
	Lifecycle         string       `json:"lifecycle"`
	Generation        int64        `json:"generation"`
	Watch             *Watch       `json:"root_watch"`
	Work              Work         `json:"work"`
	Latest            *Observation `json:"latest_observation"`
	Inventory         Inventory    `json:"inventory"`
	Checks            []Check      `json:"checks"`
	// Reader-only control flags never serialize untrusted database values.
	Mirror            bool `json:"-"`
	MirrorRetryPolicy bool `json:"-"`
}

func New(root string, o Options) Report {
	return Report{Version: 1, RootID: root, Overall: "UNKNOWN", RowLimit: o.RowLimit, StaleAfterSeconds: int64(o.StaleAfter / time.Second), Work: Work{Counts: map[string]int{}}, Checks: []Check{}}
}
func (r *Report) Add(code, status, reason string) {
	r.Checks = append(r.Checks, Check{code, status, reason})
}
func (r *Report) Finish() {
	r.Overall = "OK"
	for _, c := range r.Checks {
		if c.Status == "WARN" || c.Status == "ERROR" {
			r.Overall = "DEGRADED"
			return
		}
		if c.Status == "UNKNOWN" || c.Status == "NOT_TRACKED" {
			r.Overall = "UNKNOWN"
		}
	}
}

// Evaluate only describes persisted evidence at AsOf. No fresh provider truth,
// daemon readiness or downstream consumer acknowledgement can be inferred here.
func (r *Report) Evaluate(o Options) {
	if r.Lifecycle != "ACTIVE" {
		r.Add("ROOT_INACTIVE", "WARN", "Root is not ACTIVE; no transition attempted")
	} else {
		r.Add("ROOT_ACTIVE", "OK", "Persisted lifecycle is ACTIVE")
	}
	w := r.Watch
	if w == nil {
		r.Add("WATCH_MISSING", "UNKNOWN", "No root-scope watch; descendant schedules are not inferred")
	} else if w.State == "DISABLED" || w.State == "COLD" {
		r.Add("WATCH_DISABLED", "WARN", "Root watch has no periodic schedule")
	} else {
		due := w.NextDueAt
		if w.DeferredUntil != nil && (due == nil || w.DeferredUntil.After(*due)) {
			due = w.DeferredUntil
		}
		if due != nil && due.Before(r.AsOf.Add(-o.StaleAfter)) {
			r.Add("WATCH_LAG", "WARN", "Effective due time exceeds operator stale-after threshold")
		} else if due != nil && !due.After(r.AsOf) {
			r.Add("WATCH_DUE", "OK", "Watch is due; this command will not run it")
		} else {
			r.Add("WATCH_SCHEDULED", "OK", "Watch is scheduled or deferred")
		}
		if w.Failures > 0 {
			r.Add("WATCH_FAILURE", "WARN", "Persisted watch attempts include consecutive failures")
		}
		if sourceError(w.LastError) && w.Failures > 0 {
			r.Add("SOURCE_UNAVAILABLE", "WARN", "Last watch failure reports provider/auth failure; not a current probe")
		}
	}
	if r.Work.SourceFailures > 0 {
		r.Add("SOURCE_UNAVAILABLE_WORK", "WARN", "Work retains provider/auth failure; current availability is unknown")
	}
	if r.Work.Counts["BLOCKED"] > 0 || r.Work.Counts["SUSPENDED"] > 0 {
		r.Add("BLOCKED_WORK", "WARN", "Blocked or suspended work requires operator investigation")
	}
	if r.Work.InternalRetryCap > 0 {
		r.Add("RETRY_BUDGET_EXHAUSTED", "WARN", "Opted-in mirror root INTERNAL failures reached ADR-005 cap; no retry performed")
	}
	if r.Work.Counts["RETRY_WAIT"] > 0 {
		r.Add("RETRY_WAIT", "WARN", "Durable retry wait exists; eligibility is not a recovery authorization")
	}
	if r.Work.Counts["IN_FLIGHT"] > 0 {
		r.Add("IN_FLIGHT", "UNKNOWN", "Work is claimed; liveness/ownership cannot be established from this snapshot")
	}
	if r.Work.OldestInflightAt != nil && r.Work.OldestInflightAt.Before(r.AsOf.Add(-o.StaleAfter)) {
		r.Add("STALE_IN_FLIGHT", "WARN", "In-flight start exceeds operator threshold; no claim is reclaimed")
	}
	if r.Work.OldestDueAt != nil && r.Work.OldestDueAt.Before(r.AsOf.Add(-o.StaleAfter)) {
		r.Add("WORK_LAG", "WARN", "Pending/retry due time exceeds operator threshold")
	}
	if !r.Work.Exact {
		r.Add("WORK_LIMIT", "UNKNOWN", "Work totals are lower bounds because row cap was reached")
	}
	if r.Latest == nil {
		r.Add("OBSERVATION_MISSING", "UNKNOWN", "No persisted source observation; failed calls may create no snapshot")
	} else {
		s := r.Latest
		if s.At.After(r.AsOf.Add(30 * time.Second)) {
			r.Add("OBSERVATION_FUTURE", "WARN", "Observation is ahead of database snapshot time")
		} else if s.At.Before(r.AsOf.Add(-o.StaleAfter)) || s.Freshness == "STALE" {
			r.Add("SOURCE_STALE", "WARN", "Latest observation exceeds operator freshness threshold or is marked STALE")
		} else {
			r.Add("OBSERVATION_RECENT", "OK", "Latest persisted observation is recent; this is not whole-root freshness")
		}
		if s.Traversal == "FAILED" || s.Traversal == "INTERRUPTED" || s.Acceptance == "FAILED" {
			r.Add("OBSERVATION_FAILED", "WARN", "Latest persisted observation failed or was interrupted")
		}
		if s.Completeness != "COMPLETE" || s.Acceptance != "COMPLETE" {
			r.Add("INCOMPLETE_LISTING", "UNKNOWN", "Latest observation is not whole-root COMPLETE; normal scoped PARTIAL is not a failure")
		}
		if s.Scope != "ROOT" {
			r.Add("ROOT_FRESHNESS", "UNKNOWN", "Latest observation does not establish complete root freshness")
		}
		if r.Mirror && (!s.HistoricalCoverageV2 || !s.Reconciled) {
			r.Add("TOPOLOGY_UNTRUSTED", "UNKNOWN", "No reconciled v2 coverage on latest observation; rejection reason/current topology not tracked")
		} else if s.HistoricalCoverageV2 {
			r.Add("HISTORICAL_COVERAGE", "OK", "V2 proof recorded historically; no current topology or deletion authority asserted")
		}
	}
	if r.Inventory.DuplicatePresentPaths > 0 || r.Inventory.LatestConflictCount > 0 {
		r.Add("IDENTITY_AMBIGUITY", "WARN", "Duplicate PRESENT paths or latest durable reconcile conflicts exist; no identities merged")
	} else if !r.Inventory.CanonicalExact {
		r.Add("IDENTITY_LIMIT", "UNKNOWN", "Canonical row cap prevents full duplicate-path assessment")
	} else {
		r.Add("IDENTITY_CHECK", "OK", "No duplicate PRESENT paths in complete bounded inventory; not a general identity proof")
	}
	if r.Inventory.Mismatch {
		r.Add("CANONICAL_JOURNAL_MISMATCH", "ERROR", "Persisted generation, sequence or terminal presence differs; no repair attempted")
	} else if !r.Inventory.CanonicalExact || !r.Inventory.JournalExact {
		r.Add("CONSISTENCY_LIMIT", "UNKNOWN", "Row cap prevents full Canonical/Journal reconciliation")
	} else {
		r.Add("BOUNDED_CONSISTENCY", "OK", "Generation bounds, event sequence and terminal presence checks passed; not a payload audit")
	}
	r.Add("SOURCE_LIVE", "UNKNOWN", "No provider call; current source availability is not established")
	r.Add("PROCESS_READINESS", "NOT_TRACKED", "One-shot diagnostic does not observe the serve process or readyz")
	r.Add("CONSUMER_PROJECTION_LAG", "NOT_TRACKED", "IndexCore has no downstream Q8 acknowledgement/projection cursor")
	r.Finish()
}
func sourceError(s string) bool {
	return s == "TRANSIENT_PROVIDER" || s == "THROTTLED" || s == "AUTH_OR_PERMISSION"
}
