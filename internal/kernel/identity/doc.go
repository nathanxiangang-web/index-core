// Package identity implements the final, Kernel-owned evaluated-Snapshot IO3
// identity (Issue #44 P3): the DETERMINISTIC_DIGEST over the normalized entry
// set plus all reconcile-decision-relevant evidence, including Kernel-derived
// decision evidence such as scope_shrink_corroboration. Wall-clock, admission
// and DB timing are excluded. The digest's canonicalization namespace/version
// are Kernel-owned (doc A Sec 3.8, doc E Sec 2.4.2/2.4.3).
package identity
