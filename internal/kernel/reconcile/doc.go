// Package reconcile implements the Safe Reconcile core (Issue #44 P4):
// ADD/UPDATE/RENAME/MOVE/UNCHANGED/removal-evidence transitions, deterministic
// same-generation RENAME/MOVE + UPDATE ordering, MISSING on incomplete input
// without advancing removal evidence, confirmed removal tombstones, R8 path
// overlap, and idempotent/stale-input handling (Gate 1B Safe Reconcile).
package reconcile