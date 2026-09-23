// Package completeness implements Kernel-owned Snapshot acceptance classification
// (Issue #44 P3). It derives acceptance_state (COMPLETE/PARTIAL/FAILED/STALE/
// SUSPICIOUS) from Collector evidence per the frozen gates C-1..C-10/C-9a, and
// derives scope_shrink_corroboration from independent admitted observations.
// The Collector never sets the final verdict (doc A Sec 3.5, Gate 1B Sec 3).
package completeness
